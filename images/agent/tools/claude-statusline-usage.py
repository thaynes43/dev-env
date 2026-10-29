#!/usr/bin/env python3
"""Opt-in bridge from a Claude statusline callback to agent-run.

The image may install this file. Nothing invokes it until a caller passes an
absolute --output path. It does not edit settings or replace a display command.
"""

import os
import stat
import subprocess
import sys

MAX_BYTES = 256 * 1024
MAX_DEPTH = 32
MAX_KEYS = 64
MAX_ARRAY = 128
MAX_STRING = 8192
USAGE = b"claude-statusline-usage: expected --output PATH [--display CMD ARG...]\n"
CAPTURE = b"claude-statusline-usage: capture unavailable\n"
CONSUMER = "/usr/local/bin/agent-run"
# Simple-fold steps that join an ASCII letter. Matched to Go's asciiFold and caseOrbit.
FOLD = {
    0x6B: 0x212A,
    0x73: 0x017F,
    0xB5: 0x39C,
    0xC5: 0xE5,
    0xDF: 0x1E9E,
    0xE5: 0x212B,
    0x130: 0x130,
    0x131: 0x131,
    0x17F: 0x53,
    0x1C4: 0x1C5,
    0x1C5: 0x1C6,
    0x1C6: 0x1C4,
    0x1C7: 0x1C8,
    0x1C8: 0x1C9,
    0x1C9: 0x1C7,
    0x1CA: 0x1CB,
    0x1CB: 0x1CC,
    0x1CC: 0x1CA,
    0x1F1: 0x1F2,
    0x1F2: 0x1F3,
    0x1F3: 0x1F1,
    0x345: 0x399,
    0x390: 0x1FD3,
    0x392: 0x3B2,
    0x395: 0x3B5,
    0x398: 0x3B8,
    0x399: 0x3B9,
    0x39A: 0x3BA,
    0x39C: 0x3BC,
    0x3A0: 0x3C0,
    0x3A1: 0x3C1,
    0x3A3: 0x3C2,
    0x3A6: 0x3C6,
    0x3A9: 0x3C9,
    0x3B0: 0x1FE3,
    0x3B2: 0x3D0,
    0x3B5: 0x3F5,
    0x3B8: 0x3D1,
    0x3B9: 0x1FBE,
    0x3BA: 0x3F0,
    0x3BC: 0xB5,
    0x3C0: 0x3D6,
    0x3C1: 0x3F1,
    0x3C2: 0x3C3,
    0x3C3: 0x3A3,
    0x3C6: 0x3D5,
    0x3C9: 0x2126,
    0x3D0: 0x392,
    0x3D1: 0x3F4,
    0x3D5: 0x3A6,
    0x3D6: 0x3A0,
    0x3F0: 0x39A,
    0x3F1: 0x3A1,
    0x3F4: 0x398,
    0x3F5: 0x395,
    0x412: 0x432,
    0x414: 0x434,
    0x41E: 0x43E,
    0x421: 0x441,
    0x422: 0x442,
    0x42A: 0x44A,
    0x432: 0x1C80,
    0x434: 0x1C81,
    0x43E: 0x1C82,
    0x441: 0x1C83,
    0x442: 0x1C84,
    0x44A: 0x1C86,
    0x462: 0x463,
    0x463: 0x1C87,
    0x1C80: 0x412,
    0x1C81: 0x414,
    0x1C82: 0x41E,
    0x1C83: 0x421,
    0x1C84: 0x1C85,
    0x1C85: 0x422,
    0x1C86: 0x42A,
    0x1C87: 0x462,
    0x1C88: 0xA64A,
    0x1E60: 0x1E61,
    0x1E61: 0x1E9B,
    0x1E9B: 0x1E60,
    0x1E9E: 0xDF,
    0x1FBE: 0x345,
    0x1FD3: 0x390,
    0x1FE3: 0x3B0,
    0x2126: 0x3A9,
    0x212A: 0x4B,
    0x212B: 0xC5,
    0xA64A: 0xA64B,
    0xA64B: 0x1C88,
    0xFB05: 0xFB06,
    0xFB06: 0xFB05,
}


class Refuse(Exception):
    pass


def ascii_digit(char):
    return "0" <= char <= "9"


def owner_private(mode, required):
    bits = stat.S_IMODE(mode)
    return bits & 0o077 == 0 and bits & required == required


def simple_fold(cp):
    if cp < 128:
        folded = FOLD.get(cp)
        if folded is not None:
            return folded
        if 0x41 <= cp <= 0x5A:
            return cp + 0x20
        if 0x61 <= cp <= 0x7A:
            return cp - 0x20
        return cp
    folded = FOLD.get(cp)
    if folded is not None:
        return folded
    char = chr(cp)
    lower = char.lower()
    if len(lower) == 1 and ord(lower) != cp:
        return ord(lower)
    upper = char.upper()
    if len(upper) == 1:
        return ord(upper)
    return cp


def equal_fold(left, right):
    i = j = 0
    while i < len(left) and j < len(right):
        sr = ord(left[i])
        tr = ord(right[j])
        i += 1
        j += 1
        if sr == tr:
            continue
        if tr < sr:
            sr, tr = tr, sr
        if tr < 128:
            if 0x41 <= sr <= 0x5A and tr == sr + 0x20:
                continue
            return False
        folded = simple_fold(sr)
        for _ in range(8):
            if folded == sr or folded >= tr:
                break
            folded = simple_fold(folded)
        if folded != tr:
            return False
    return i == len(left) and j == len(right)


def reject_fold(keys, recognized):
    for key in keys:
        for want in recognized:
            if key != want and equal_fold(key, want):
                raise Refuse


class Parser:
    def __init__(self, text):
        self.text = text
        self.n = len(text)
        self.i = 0

    def peek(self):
        if self.i >= self.n:
            raise Refuse
        return self.text[self.i]

    def skip(self):
        while self.i < self.n and self.text[self.i] in " \t\r\n":
            self.i += 1

    def parse(self):
        self.skip()
        value = self.value(0)
        self.skip()
        if self.i != self.n:
            raise Refuse
        return value

    def value(self, depth):
        self.skip()
        char = self.peek()
        if char == "{":
            if depth + 1 > MAX_DEPTH:
                raise Refuse
            return self.object(depth + 1)
        if char == "[":
            if depth + 1 > MAX_DEPTH:
                raise Refuse
            return self.array(depth + 1)
        if char == '"':
            return ("string", self.string())
        if char == "t":
            return ("literal", self.literal("true"))
        if char == "f":
            return ("literal", self.literal("false"))
        if char == "n":
            return ("literal", self.literal("null"))
        if char == "-" or ascii_digit(char):
            return ("number", self.number())
        raise Refuse

    def literal(self, word):
        if self.text.startswith(word, self.i):
            self.i += len(word)
            return word
        raise Refuse

    def number(self):
        start = self.i
        if self.peek() == "-":
            self.i += 1
        if self.i >= self.n or not ascii_digit(self.text[self.i]):
            raise Refuse
        if self.text[self.i] == "0":
            self.i += 1
        else:
            while self.i < self.n and ascii_digit(self.text[self.i]):
                self.i += 1
        if self.i < self.n and self.text[self.i] == ".":
            self.i += 1
            if self.i >= self.n or not ascii_digit(self.text[self.i]):
                raise Refuse
            while self.i < self.n and ascii_digit(self.text[self.i]):
                self.i += 1
        if self.i < self.n and self.text[self.i] in "eE":
            self.i += 1
            if self.i < self.n and self.text[self.i] in "+-":
                self.i += 1
            if self.i >= self.n or not ascii_digit(self.text[self.i]):
                raise Refuse
            while self.i < self.n and ascii_digit(self.text[self.i]):
                self.i += 1
        raw = self.text[start:self.i]
        if len(raw.encode("utf-8")) > MAX_STRING:
            raise Refuse
        return raw

    def string(self):
        if self.peek() != '"':
            raise Refuse
        self.i += 1
        chars = []
        while self.i < self.n:
            char = self.text[self.i]
            self.i += 1
            if char == '"':
                decoded = "".join(chars)
                if len(decoded.encode("utf-8")) > MAX_STRING:
                    raise Refuse
                return decoded
            if char == "\\":
                chars.append(self.escape())
                continue
            if ord(char) < 0x20:
                raise Refuse
            chars.append(char)
        raise Refuse

    def escape(self):
        if self.i >= self.n:
            raise Refuse
        char = self.text[self.i]
        self.i += 1
        if char == "u":
            return self.unicode_escape()
        mapped = {"\"": "\"", "\\": "\\", "/": "/", "b": "\b", "f": "\f", "n": "\n", "r": "\r", "t": "\t"}.get(char)
        if mapped is None:
            raise Refuse
        return mapped

    def unicode_escape(self):
        code = self.hex4()
        if 0xDC00 <= code <= 0xDFFF:
            raise Refuse
        if 0xD800 <= code <= 0xDBFF:
            if not self.text.startswith("\\u", self.i):
                raise Refuse
            self.i += 2
            low = self.hex4()
            if not 0xDC00 <= low <= 0xDFFF:
                raise Refuse
            code = 0x10000 + ((code - 0xD800) << 10) + (low - 0xDC00)
        return chr(code)

    def hex4(self):
        chunk = self.text[self.i:self.i + 4]
        if len(chunk) != 4 or any(c not in "0123456789abcdefABCDEF" for c in chunk):
            raise Refuse
        self.i += 4
        return int(chunk, 16)

    def object(self, depth):
        self.i += 1
        pairs = []
        seen = set()
        self.skip()
        if self.i < self.n and self.text[self.i] == "}":
            self.i += 1
            return ("object", pairs)
        count = 0
        while True:
            self.skip()
            key = self.string()
            if key in seen:
                raise Refuse
            seen.add(key)
            count += 1
            if count > MAX_KEYS:
                raise Refuse
            self.skip()
            if self.peek() != ":":
                raise Refuse
            self.i += 1
            pairs.append((key, self.value(depth)))
            self.skip()
            char = self.peek()
            if char == ",":
                self.i += 1
                continue
            if char == "}":
                self.i += 1
                return ("object", pairs)
            raise Refuse

    def array(self, depth):
        self.i += 1
        items = []
        self.skip()
        if self.i < self.n and self.text[self.i] == "]":
            self.i += 1
            return ("array", items)
        count = 0
        while True:
            count += 1
            if count > MAX_ARRAY:
                raise Refuse
            items.append(self.value(depth))
            self.skip()
            char = self.peek()
            if char == ",":
                self.i += 1
                continue
            if char == "]":
                self.i += 1
                return ("array", items)
            raise Refuse


def is_null(node):
    return node == ("literal", "null")


def exact(pairs, key):
    for name, value in pairs:
        if name == key:
            return value
    return None


def project(root):
    if root[0] != "object":
        raise Refuse
    pairs = root[1]
    reject_fold([key for key, _ in pairs], ("rate_limits",))
    window = exact(pairs, "rate_limits")
    if window is None or is_null(window):
        return b"{}"
    if window[0] != "object":
        raise Refuse
    reject_fold([key for key, _ in window[1]], ("five_hour", "seven_day"))
    parts = []
    for name in ("five_hour", "seven_day"):
        item = exact(window[1], name)
        if item is None or is_null(item):
            continue
        if item[0] != "object":
            raise Refuse
        reject_fold([key for key, _ in item[1]], ("used_percentage", "resets_at"))
        fields = []
        for field in ("used_percentage", "resets_at"):
            value = exact(item[1], field)
            if value is None or is_null(value):
                continue
            if value[0] != "number":
                raise Refuse
            fields.append('"%s":%s' % (field, value[1]))
        parts.append('"%s":{%s}' % (name, ",".join(fields)))
    return ('{"rate_limits":{%s}}' % ",".join(parts)).encode("ascii")


def consumer_argv():
    return [CONSUMER, "usage", "-o", "json", "--claude-statusline"]


def run_consumer(payload):
    try:
        proc = subprocess.run(consumer_argv(), input=payload, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False)
    except OSError:
        raise Refuse
    if proc.returncode != 0 or not proc.stdout or len(proc.stdout) > MAX_BYTES:
        raise Refuse
    return proc.stdout


def publish(dest, data):
    if not dest.startswith("/") or "\x00" in dest:
        raise Refuse
    nofollow = getattr(os, "O_NOFOLLOW", 0)
    if not nofollow:
        raise Refuse
    parts = dest.split("/")
    if any(part in ("", ".", "..") for part in parts[1:]):
        raise Refuse
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    tmp = ""
    try:
        for part in parts[1:-1]:
            try:
                nxt = os.open(part, os.O_RDONLY | nofollow | os.O_DIRECTORY | os.O_CLOEXEC, dir_fd=fd)
            except OSError:
                raise Refuse
            os.close(fd)
            fd = nxt
        info = os.fstat(fd)
        if info.st_uid != os.geteuid() or not owner_private(info.st_mode, 0o700):
            raise Refuse
        name = parts[-1]
        try:
            current = os.stat(name, dir_fd=fd, follow_symlinks=False)
        except FileNotFoundError:
            current = None
        except OSError:
            raise Refuse
        if current is not None and (not stat.S_ISREG(current.st_mode) or current.st_uid != os.geteuid() or not owner_private(current.st_mode, 0o600)):
            raise Refuse
        tmp = ".csu-%s" % os.urandom(8).hex()
        # O_EXCL does not follow an existing symlink. O_NOFOLLOW with O_CREAT is not required here.
        out = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC, 0o600, dir_fd=fd)
        try:
            os.fchmod(out, 0o600)
            view = memoryview(data)
            while view:
                wrote = os.write(out, view)
                if wrote <= 0:
                    raise Refuse
                view = view[wrote:]
            os.fsync(out)
        finally:
            os.close(out)
        os.rename(tmp, name, src_dir_fd=fd, dst_dir_fd=fd)
        tmp = ""
        try:
            os.fsync(fd)
        except OSError:
            pass
    except Refuse:
        if tmp:
            try:
                os.unlink(tmp, dir_fd=fd)
            except OSError:
                pass
        raise
    except OSError:
        if tmp:
            try:
                os.unlink(tmp, dir_fd=fd)
            except OSError:
                pass
        raise Refuse
    finally:
        os.close(fd)


def capture(data, output):
    if len(data) > MAX_BYTES or data.startswith(b"\xef\xbb\xbf"):
        raise Refuse
    try:
        text = data.decode("utf-8")
    except UnicodeError:
        raise Refuse
    filtered = project(Parser(text).parse())
    publish(output, run_consumer(filtered))


def read_capture_only(stream):
    # One bounded read. Do not wait for EOF after the limit is known.
    data = stream.read(MAX_BYTES + 1)
    if len(data) > MAX_BYTES:
        return b"", True
    return data, False


def write_all(pipe, chunk):
    view = memoryview(chunk)
    while view:
        wrote = pipe.write(view)
        if wrote <= 0:
            raise OSError
        view = view[wrote:]


def forward_display(argv, stream):
    try:
        proc = subprocess.Popen(argv, stdin=subprocess.PIPE)
    except OSError:
        data, overflow = read_capture_only(stream)
        return data, overflow, 127
    kept = bytearray()
    overflow = False
    complete = False
    try:
        while True:
            chunk = stream.read(65536)
            if not chunk:
                complete = True
                break
            if not overflow:
                if len(kept) + len(chunk) > MAX_BYTES:
                    overflow = True
                    kept.clear()
                else:
                    kept.extend(chunk)
            write_all(proc.stdin, chunk)
    except OSError:
        # The display closed early, or the source read failed. The unread
        # remainder is not a confirmed document, so the retained prefix is not
        # captured. Do not keep reading and do not kill the display.
        complete = False
    try:
        proc.stdin.close()
    except OSError:
        pass
    code = proc.wait()
    if complete and not overflow:
        return bytes(kept), False, code
    return b"", True, code


def parse_args(argv):
    if len(argv) < 3 or argv[1] != "--output" or not argv[2] or argv[2].startswith("-"):
        raise Refuse
    display = None
    rest = argv[3:]
    if rest:
        if rest[0] != "--display" or len(rest) < 2:
            raise Refuse
        display = rest[1:]
        if display[0] == "--":
            display = display[1:]
        if not display or any(part == "" for part in display):
            raise Refuse
    return argv[2], display


def main(argv):
    try:
        output, display = parse_args(argv)
    except Refuse:
        sys.stderr.buffer.write(USAGE)
        return 2
    stream = sys.stdin.buffer
    code = None
    try:
        if display:
            data, overflow, code = forward_display(display, stream)
        else:
            data, overflow = read_capture_only(stream)
    except OSError:
        data, overflow = b"", True
    ok = False
    if not overflow:
        try:
            capture(data, output)
            ok = True
        except Exception:
            ok = False
    if not ok:
        try:
            sys.stderr.buffer.write(CAPTURE)
        except OSError:
            pass
    if code is not None:
        return code if code >= 0 else 1
    return 0 if ok else 1


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv))
    except Exception:
        try:
            sys.stderr.buffer.write(CAPTURE)
        except OSError:
            pass
        sys.exit(1)
