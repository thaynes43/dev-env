/* SPDX-License-Identifier: GPL-2.0-only
 * Fixture-only ABI. All accesses to shared storage are lock-free C11 atomics.
 * No paths, arguments, pointers or process identities occur in the ABI.
 */
#define _GNU_SOURCE
#include "marker.h"
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#if !defined(__linux__) || !defined(__x86_64__)
#error "git-wait ABI is reviewed only for Linux AMD64"
#endif
enum { MAGIC = 0x47574d31, VERSION = 1, BYTES = 64 };
enum { STATE_IDLE = 0, STATE_ACTIVE = 1 };
enum { FAULT_OVERLAP = 1, FAULT_WRAP = 2, FAULT_STATE = 3 };
struct marker {
	_Atomic uint32_t magic, version, bytes, sequence, state, operation;
	_Atomic uint32_t operation_claim, scope_claim, fault, reserved[7];
};
_Static_assert(sizeof(_Atomic uint32_t) == 4, "atomic word ABI");
_Static_assert(ATOMIC_INT_LOCK_FREE == 2, "always lock-free int atomics required");
_Static_assert(_Alignof(_Atomic uint32_t) == 4, "atomic alignment ABI");
_Static_assert(sizeof(struct marker) == BYTES, "exact marker ABI");
_Static_assert(offsetof(struct marker, sequence) == 12, "sequence ABI");
_Static_assert(offsetof(struct marker, fault) == 32, "fault ABI");
_Static_assert(offsetof(struct marker, reserved) == 36, "reserved ABI");
/* Default seq_cst ordering intentionally avoids a weaker cross-process protocol. */
static int supported(struct marker *m)
{
	return atomic_is_lock_free(&m->sequence) && atomic_is_lock_free(&m->fault);
}
static int valid(struct marker *m)
{
	unsigned i;
	if (atomic_load(&m->magic) != MAGIC || atomic_load(&m->version) != VERSION ||
	    atomic_load(&m->bytes) != BYTES)
		return 0;
	for (i = 0; i < 7; i++)
		if (atomic_load(&m->reserved[i]))
			return 0;
	return 1;
}
static struct marker *map_fd(int fd, int writable)
{
	struct stat st;
	void *p;
	int seals = F_SEAL_SHRINK | F_SEAL_GROW | F_SEAL_SEAL;
	int actual_seals = fcntl(fd, F_GET_SEALS);
	if (fstat(fd, &st) || !S_ISREG(st.st_mode) || st.st_size != BYTES ||
	    actual_seals < 0 || (actual_seals & seals) != seals)
		return NULL;
	p = mmap(NULL, BYTES, PROT_READ | (writable ? PROT_WRITE : 0), MAP_SHARED, fd, 0);
	if (p == MAP_FAILED)
		return NULL;
	if (!supported(p)) {
		munmap(p, BYTES);
		return NULL;
	}
	return p;
}
void *gwm_parent_create(int fd)
{
	struct marker *m = map_fd(fd, 1);
	unsigned i;
	if (!m)
		return NULL;
	atomic_init(&m->magic, MAGIC);
	atomic_init(&m->version, VERSION);
	atomic_init(&m->bytes, BYTES);
	atomic_init(&m->sequence, 0);
	atomic_init(&m->state, STATE_IDLE);
	atomic_init(&m->operation, GWM_IDLE);
	atomic_init(&m->operation_claim, 0);
	atomic_init(&m->scope_claim, 0);
	atomic_init(&m->fault, 0);
	for (i = 0; i < 7; i++)
		atomic_init(&m->reserved[i], 0);
	return m;
}
void gwm_parent_close(void *mapping)
{
	if (mapping)
		munmap(mapping, BYTES);
}
#ifdef GWM_TESTING
static int change_during_snapshot;
void gwm_test_change_during_snapshot(int enabled) { change_during_snapshot = enabled; }
void gwm_test_word(void *mapping, unsigned word, uint32_t value)
{
	struct marker *m = mapping;
	/* Deterministic malformed/coherence fixtures, never a racing test loop. */
	switch (word) {
	case 1: atomic_store(&m->version, value); break;
	case 3: atomic_store(&m->sequence, value); break;
	case 4: atomic_store(&m->state, value); break;
	case 5: atomic_store(&m->operation, value); break;
	default: abort();
	}
}
#endif
int gwm_snapshot(void *mapping, uint32_t *operation)
{
	struct marker *m = mapping;
	uint32_t before, after, state, op, fault, claim, scope;
	*operation = GWM_IDLE;
	if (!m)
		return GWM_UNAVAILABLE;
	if (!supported(m))
		return GWM_UNSUPPORTED;
	if (!valid(m))
		return GWM_INVALID;
	before = atomic_load(&m->sequence);
	if (before >= UINT32_MAX - 4)
		return GWM_WRAP;
	if (before & 1)
		return GWM_ODD;
	state = atomic_load(&m->state);
	op = atomic_load(&m->operation);
	fault = atomic_load(&m->fault);
	claim = atomic_load(&m->operation_claim);
	scope = atomic_load(&m->scope_claim);
#ifdef GWM_TESTING
	if (change_during_snapshot)
		atomic_fetch_add(&m->sequence, 2);
#endif
	after = atomic_load(&m->sequence);
	if (before != after || (after & 1))
		return GWM_CHANGED;
	/* Fault publication is independent of an admitted writer's sequence. */
	if (fault || atomic_load(&m->fault))
		return GWM_FAULT;
	if (state == STATE_IDLE && op == GWM_IDLE)
		return GWM_NO_OPERATION;
	if (state != STATE_ACTIVE || !claim || !scope ||
	    op < GWM_REFRESH_LSTAT || op > GWM_SMALL_FILE_CONTENT_READ)
		return GWM_INVALID;
	*operation = op;
	return GWM_COHERENT;
}

#ifndef GWM_READER_ONLY
static struct marker *writer;
static _Thread_local int in_scope;
/* A forked child cannot publish through its inherited map; exec closes the FD. */
static void fork_child(void) { writer = NULL; in_scope = 0; }
static void initialize_writer(void) __attribute__((constructor));
static void initialize_writer(void)
{
	int saved = errno, fd, flags;
	const char *text = getenv("DEV_ENV_GIT_WAIT_FD");
	char *end;
	long value;
	if (!text || !*text)
		goto done;
	errno = 0;
	value = strtol(text, &end, 10);
	if (errno || *end || value < 3 || value > INT_MAX)
		goto done;
	fd = (int)value;
	flags = fcntl(fd, F_GETFD);
	if (flags < 0 || fcntl(fd, F_SETFD, flags | FD_CLOEXEC) < 0)
		goto done;
	writer = map_fd(fd, 1);
	if (writer && (!valid(writer) || pthread_atfork(NULL, NULL, fork_child))) {
		munmap(writer, BYTES);
		writer = NULL;
	}
done:
	errno = saved;
}
#ifdef GWM_TESTING
void gwm_test_attach(void *mapping) { writer = mapping; in_scope = 0; }
#endif
static int scope_enter(void)
{
	uint32_t expected = 0;
	if (!writer || atomic_load(&writer->fault))
		return 0;
	if (in_scope || !atomic_compare_exchange_strong(&writer->scope_claim, &expected, 1)) {
		atomic_store(&writer->fault, FAULT_OVERLAP);
		return 0;
	}
	in_scope = 1;
	return 1;
}
static void scope_leave(int admitted)
{
	if (!admitted || !writer)
		return;
	if (atomic_load(&writer->operation_claim))
		atomic_store(&writer->fault, FAULT_STATE);
	in_scope = 0;
	atomic_store(&writer->scope_claim, 0);
}
static int transition(uint32_t state, uint32_t operation)
{
	uint32_t seq = atomic_load(&writer->sequence);
	if ((seq & 1) || seq >= UINT32_MAX - 4) {
		atomic_store(&writer->fault, (seq & 1) ? FAULT_STATE : FAULT_WRAP);
		return 0;
	}
	atomic_store(&writer->sequence, seq + 1);
	atomic_store(&writer->state, state);
	atomic_store(&writer->operation, operation);
	atomic_store(&writer->sequence, seq + 2);
	return 1;
}
static int begin(unsigned operation)
{
	uint32_t expected = 0;
	if (!writer || !in_scope || atomic_load(&writer->fault))
		return 0;
	if (operation < GWM_REFRESH_LSTAT || operation > GWM_SMALL_FILE_CONTENT_READ ||
	    !atomic_compare_exchange_strong(&writer->operation_claim, &expected, 1)) {
		atomic_store(&writer->fault, FAULT_OVERLAP);
		return 0;
	}
	if (!transition(STATE_ACTIVE, operation)) {
		atomic_store(&writer->operation_claim, 0);
		return 0;
	}
	return 1;
}
static void end(int admitted)
{
	if (!admitted || !writer)
		return;
	/* Even after overlap fault, clear only the original admitted operation. */
	transition(STATE_IDLE, GWM_IDLE);
	atomic_store(&writer->operation_claim, 0);
}
/* Preserve the wrapped Git call's errno on every admitted/refused/fault path. */
int gwm_scope_enter(void)
{
	int saved = errno;
	int result = scope_enter();
	errno = saved;
	return result;
}
void gwm_scope_leave(int admitted)
{
	int saved = errno;
	scope_leave(admitted);
	errno = saved;
}
int gwm_begin(unsigned operation)
{
	int saved = errno;
	int result = begin(operation);
	errno = saved;
	return result;
}
void gwm_end(int admitted)
{
	int saved = errno;
	end(admitted);
	errno = saved;
}
#endif
