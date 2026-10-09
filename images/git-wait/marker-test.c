/* SPDX-License-Identifier: GPL-2.0-only
 * Hosted-only finite fixtures. No spin/stress/racing loop or public child PID.
 */
#define _GNU_SOURCE
#include "marker.h"
#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static void *mapping;
static int fd;
static void reset(void)
{
	if (mapping)
		gwm_parent_close(mapping);
	mapping = gwm_parent_create(fd);
	assert(mapping);
	gwm_test_attach(mapping);
}
static int sample(uint32_t *op) { return gwm_snapshot(mapping, op); }
static void *competing_scope(void *unused)
{
	(void)unused;
	assert(!gwm_scope_enter());
	return NULL;
}
int main(int argc, char **argv)
{
	uint32_t op;
	unsigned operation;
	int scope, active;
	pthread_t other;
	if (argc == 2 && !strcmp(argv[1], "--fork-disabled")) {
		pid_t child;
		int status;
		scope = gwm_scope_enter();
		active = gwm_begin(GWM_REFRESH_LSTAT);
		assert(scope && active);
		child = fork();
		assert(child >= 0);
		if (!child)
			_exit(gwm_scope_enter() || gwm_begin(GWM_REFRESH_LSTAT));
		assert(waitpid(child, &status, 0) == child && WIFEXITED(status) && !WEXITSTATUS(status));
		gwm_end(active);
		gwm_scope_leave(scope);
		return 0;
	}
	if (argc == 2 && !strcmp(argv[1], "--owned-sleep")) {
		/* One parent-owned sleeping test child for the real 5s checkpoint/reap. */
		struct timespec delay = { 6, 0 };
		scope = gwm_scope_enter();
		active = gwm_begin(GWM_CONTENT_OPEN_WRAPPER);
		assert(scope && active);
		nanosleep(&delay, NULL);
		gwm_end(active);
		gwm_scope_leave(scope);
		return 0;
	}
	assert(argc == 1);
	fd = memfd_create("git-wait-test", MFD_CLOEXEC | MFD_ALLOW_SEALING);
	assert(fd >= 0 && !ftruncate(fd, 64));
	assert(!gwm_parent_create(fd)); /* Unsealed storage refuses. */
	assert(!fcntl(fd, F_ADD_SEALS, F_SEAL_SHRINK | F_SEAL_GROW | F_SEAL_SEAL));
	reset();
	assert(sample(&op) == GWM_NO_OPERATION && op == GWM_IDLE);
	assert(!gwm_begin(GWM_REFRESH_LSTAT)); /* Generic reads are outside refresh. */
	for (operation = GWM_REFRESH_LSTAT; operation <= GWM_SMALL_FILE_CONTENT_READ; operation++) {
		errno = EIO;
		scope = gwm_scope_enter();
		active = gwm_begin(operation);
		assert(scope && active && errno == EIO);
		assert(sample(&op) == GWM_COHERENT && op == operation);
		{
			char byte;
			assert(read(-1, &byte, 1) == -1 && errno == EBADF);
		}
		gwm_end(active);
		gwm_scope_leave(scope);
		assert(errno == EBADF && sample(&op) == GWM_NO_OPERATION);
	}
	gwm_test_word(mapping, 1, 2);
	assert(sample(&op) == GWM_INVALID);
	reset();
	gwm_test_word(mapping, 3, 1);
	assert(sample(&op) == GWM_ODD);
	reset();
	gwm_test_word(mapping, 3, UINT32_MAX - 4);
	assert(sample(&op) == GWM_WRAP);
	scope = gwm_scope_enter();
	assert(scope && !gwm_begin(GWM_REFRESH_LSTAT));
	gwm_scope_leave(scope);
	reset();
	scope = gwm_scope_enter();
	active = gwm_begin(GWM_REFRESH_LSTAT);
	assert(active);
	gwm_test_change_during_snapshot(1);
	assert(sample(&op) == GWM_CHANGED);
	gwm_test_change_during_snapshot(0);
	gwm_end(active);
	gwm_scope_leave(scope);
	reset();
	scope = gwm_scope_enter();
	active = gwm_begin(GWM_CONTENT_OPEN_WRAPPER);
	assert(active && !gwm_begin(GWM_SMALL_FILE_CONTENT_READ));
	assert(sample(&op) == GWM_FAULT);
	gwm_end(active);
	gwm_scope_leave(scope);
	assert(!gwm_scope_enter() && sample(&op) == GWM_FAULT);
	reset();
	scope = gwm_scope_enter();
	assert(scope && !gwm_scope_enter());
	assert(sample(&op) == GWM_FAULT);
	gwm_scope_leave(scope);
	reset();
	scope = gwm_scope_enter();
	active = gwm_begin(GWM_REFRESH_LSTAT);
	assert(active && !pthread_create(&other, NULL, competing_scope, NULL));
	assert(!pthread_join(other, NULL));
	assert(sample(&op) == GWM_FAULT);
	gwm_end(active);
	gwm_scope_leave(scope);
	reset();
	gwm_test_word(mapping, 4, 1);
	gwm_test_word(mapping, 5, GWM_REFRESH_LSTAT);
	assert(sample(&op) == GWM_INVALID); /* Unclaimed active state cannot report. */
	assert(gwm_snapshot(NULL, &op) == GWM_UNAVAILABLE);
	gwm_parent_close(mapping);
	assert(!close(fd));
	puts("PASS finite Git wait ABI: coherence, idle, version, odd, change, wrap, overlap, errno");
	return 0;
}
