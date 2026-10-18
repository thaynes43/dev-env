#!/bin/bash
# Offline measurement for DESIGN-024: four worst-case coord-archive gets and
# one ctl prune, in a container limited to the shelf's 0.25 CPU and 256 MiB.
# This does not mount a cluster volume, start R5, raise the shelf limit, open
# a cohort, or call kubectl.
set -euo pipefail

if [ "$#" -ne 1 ] || { [ "$1" != "arm64" ] && [ "$1" != "amd64" ]; }; then
	echo "usage: $0 arm64|amd64" >&2
	exit 2
fi
arch=$1
runs=${MEASURE_RUNS:-2}
cd "$(dirname "$0")"
root=$(git rev-parse --show-toplevel)
image=debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
stage=${MEASURE_STAGE:-/tmp/coord-concurrent-measure}
mkdir -p "$stage/archive" "$stage/bin/$arch" "$stage/logs/$arch"

export GOMAXPROCS=2
export CGO_ENABLED=0
cd "$root"
if [ ! -s "$stage/archive/receipt.json" ]; then
	echo "preparing one ceiling-sized archive"
	mkdir -p "$stage/bin/arm64"
	nice -n 19 env GOOS=linux GOARCH=arm64 go test -c -o "$stage/bin/arm64/prepare.test" ./cmd/agentd
	docker rm -f coord-measure-prepare >/dev/null 2>&1 || true
	docker run --name coord-measure-prepare --network=none --cpus=0.5 --memory=1g --memory-swap=1g \
		--platform linux/arm64 \
		--tmpfs /mnt/prepare:rw,size=64m \
		-v "$stage/bin/arm64:/opt/measure:ro" \
		-v "$stage/archive:/out" \
		-e HOME=/tmp \
		-e DEV_ENV_COORD_PREPARE_ROOT=/mnt/prepare \
		-e DEV_ENV_COORD_PREPARE_HOME=/tmp \
		-e DEV_ENV_COORD_PREPARE_RECEIPT=/out/receipt.json \
		"$image" \
		sh -c 'mkdir -p /out/root && /opt/measure/prepare.test -test.run "^TestCoordArchiveWorstCasePrepare$" -test.count=1 -test.timeout 60s && cp -a /mnt/prepare/. /out/root/'
	docker inspect coord-measure-prepare --format 'prepare_image={{.Image}} prepare_oom={{.State.OOMKilled}} prepare_exit={{.State.ExitCode}}'
	docker rm coord-measure-prepare >/dev/null
fi

echo "building $arch binaries"
nice -n 19 env GOOS=linux GOARCH="$arch" go build -buildvcs=false -o "$stage/bin/$arch/agentd" ./cmd/agentd
nice -n 19 env GOOS=linux GOARCH="$arch" go build -buildvcs=false -o "$stage/bin/$arch/coord-concurrent-measure-wait" ./cmd/agentd/coordarchive_shelf_measure_concurrent_wait.go
shasum -a 256 \
	"$root/cmd/agentd/coordarchive_shelf_measure_concurrent_wait.go" \
	"$stage/bin/$arch/agentd" \
	"$stage/bin/$arch/coord-concurrent-measure-wait"
echo "source_head=$(git rev-parse HEAD)"
python3 - "$stage/archive/receipt.json" "$stage/bin/$arch/plan.json" <<'PY'
import json, sys
receipt = json.load(open(sys.argv[1]))
name = "task-claim." + ("c" * 32) + "." + ("d" * 32)
uid = "44444444-4444-4444-8444-444444444444"
sha = receipt["sha256"]
if receipt["resource"] != "taskclaims" or receipt["name"] != name or receipt["uid"] != uid or receipt["resourceVersion"] != "42":
    raise SystemExit("receipt is not the worst-case claim")
agentd = "/opt/measure/agentd"
argv = [agentd, "ctl", "coord-archive", "get", "--resource", "taskclaims", "--name", name, "--uid", uid, "--resource-version", "42", "--sha256", sha]
cmds = [{"name": f"get-{i}", "argv": argv, "holdStdout": True, "expect": sha} for i in range(1, 5)]
cmds.append({"name": "prune", "argv": [agentd, "ctl", "prune"], "stdin": '{"olderThan":"720h","keep":[]}', "expect": "rescue/measure-old/20260801-0000"})
with open(sys.argv[2], "w") as fh:
    json.dump(cmds, fh)
PY

cat > "$stage/bin/$arch/inside.sh" <<'SH'
#!/bin/sh
set -eu
mkdir -p /logs
if ! find /home/measure/.coordination-archive -type f -print -quit | grep -q .; then
	echo "archive copy is empty" >&2
	exit 1
fi
stat -c '%d %n' /home/measure /home/measure/.coordination-archive /home/measure/.shared > /logs/mounts.txt
uname -m > /logs/uname.txt
grep -m1 -E 'model name|^Model' /proc/cpuinfo > /logs/cpu.txt || true
old=/home/measure/.shared/rescue/measure-old/20260801-0000
young=/home/measure/.shared/rescue/measure-young/20261017-1200
mkdir -p "$old" "$young" /home/measure/.shared/logs
printf 'synthetic\n' > "$old/note.txt"
printf 'synthetic\n' > /home/measure/.shared/logs/measure-old.log
printf 'synthetic\n' > "$young/note.txt"
touch -d '2026-08-01T00:00:00Z' "$old/note.txt" "$old" /home/measure/.shared/logs/measure-old.log
find /home/measure/.coordination-archive -type f | wc -l > /logs/archive-files.txt
export HOME=/home/measure
exec /opt/measure/coord-concurrent-measure-wait /opt/measure/plan.json
SH
chmod +x "$stage/bin/$arch/inside.sh"

# The archive is copied onto the tmpfs inside the container. Doing it in the
# entrypoint keeps the copy out of the waiter's address space.
cat > "$stage/bin/$arch/entry.sh" <<'SH'
#!/bin/sh
set -eu
cp -a /opt/archive/root/. /home/measure/.coordination-archive/
exec /opt/measure/inside.sh
SH
chmod +x "$stage/bin/$arch/entry.sh"

n=1
while [ "$n" -le "$runs" ]; do
	name="coord-measure-${arch}-${n}"
	log="$stage/logs/$arch/run-$n"
	mkdir -p "$log"
	docker rm -f "$name" >/dev/null 2>&1 || true
	set +e
	docker run --name "$name" --network=none --cpus=0.25 --memory=256m --memory-swap=256m \
		--platform "linux/$arch" --read-only \
		--tmpfs /tmp:rw,size=16m \
		--tmpfs /home/measure/.coordination-archive:rw,size=64m \
		--tmpfs /home/measure/.shared:rw,size=16m \
		-v "$stage/bin/$arch:/opt/measure:ro" \
		-v "$stage/archive:/opt/archive:ro" \
		-v "$log:/logs" \
		-e HOME=/home/measure \
		"$image" /opt/measure/entry.sh >"$log/stdout.txt" 2>"$log/stderr.txt"
	rc=$?
	set -e
	docker inspect "$name" --format 'image={{.Image}} oom={{.State.OOMKilled}} exit={{.State.ExitCode}} platform={{.Platform}}' | tee "$log/inspect.txt"
	docker rm "$name" >/dev/null
	echo "run $arch $n exit $rc"
	if [ "$rc" -ne 0 ]; then
		exit "$rc"
	fi
	n=$((n + 1))
done
