/* SPDX-License-Identifier: GPL-2.0-only */
#ifndef DEV_ENV_GIT_WAIT_MARKER_H
#define DEV_ENV_GIT_WAIT_MARKER_H

#include <stddef.h>
#include <stdint.h>

enum gwm_operation {
	GWM_IDLE = 0,
	GWM_REFRESH_LSTAT = 1,
	GWM_CONTENT_OPEN_WRAPPER = 2,
	GWM_SMALL_FILE_CONTENT_READ = 3
};
enum gwm_result {
	GWM_COHERENT = 0,
	GWM_UNAVAILABLE = 1,
	GWM_UNSUPPORTED = 2,
	GWM_INVALID = 3,
	GWM_ODD = 4,
	GWM_CHANGED = 5,
	GWM_WRAP = 6,
	GWM_FAULT = 7,
	GWM_NO_OPERATION = 8
};
/* The mapping is opaque. Python never copies or interprets atomic storage. */
void *gwm_parent_create(int fd);
void gwm_parent_close(void *mapping);
int gwm_snapshot(void *mapping, uint32_t *operation);
int gwm_scope_enter(void);
void gwm_scope_leave(int admitted);
int gwm_begin(unsigned operation);
void gwm_end(int admitted);

#ifdef GWM_TESTING
void gwm_test_attach(void *mapping);
void gwm_test_word(void *mapping, unsigned word, uint32_t value);
void gwm_test_change_during_snapshot(int enabled);
#endif
#endif
