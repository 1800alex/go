// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build cgo
// +build aix darwin dragonfly freebsd linux netbsd openbsd solaris

#include <pthread.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h> // strerror
#include <time.h>
#include "libcgo.h"
#include "libcgo_unix.h"
#include <unistd.h>

static pthread_cond_t runtime_init_cond = PTHREAD_COND_INITIALIZER;
static pthread_mutex_t runtime_init_mu = PTHREAD_MUTEX_INITIALIZER;
static int runtime_init_done;

// The context function, used when tracing back C calls into Go.
static void (*cgo_context_function)(struct context_arg*);

// Detect if using glibc
void *
x_cgo_sys_lib_args_valid()
{
	// The ELF gABI doesn't require an argc / argv to be passed to the functions
	// in the DT_INIT_ARRAY. However, glibc always does.
	// Ignore uClibc masquerading as glibc.
#if defined(__GLIBC__) && !defined(__UCLIBC__)
	return 0;
#else
	FILE *file = NULL;
	size_t read_bytes = -1;
	size_t size = 0;
	int block_read_bytes = 0;
	char block[128];
	int i;

	char *buffer = NULL;
	int *argc = NULL;

	file = fopen("/proc/self/cmdline", "r");
	if(NULL == file) {
		return 0;
	}

	printf("reading file\n");
	fflush(stdout);

	do {
		block_read_bytes = fread(&block, 1, (size_t)128, file);

		printf("read %d\n",block_read_bytes);
		fflush(stdout);
		usleep(100000);

		if(block_read_bytes == 0) {
			if(buffer == NULL) {
				/* Just return an empty string */
				size = 5;
				buffer = (char *)malloc(size);

				if(NULL == buffer) {
					return 0;
				}

				argc = (int *)buffer;
				*argc = 0;
				buffer[4] = 0;	/* NULL terminate */

				//buffer[0][0] = 0; /* NULL terminate */
			}
		}
		else if(block_read_bytes > 0) {
			printf("LINE: %d\n",__LINE__);fflush(stdout);usleep(100000);
			if(buffer == NULL) {
				size = block_read_bytes + 4;

				printf("LINE: %d\n",__LINE__);fflush(stdout);usleep(100000);
				buffer = (char *)malloc(size);

				if(NULL == buffer) {
					printf("LINE: %d\n",__LINE__);fflush(stdout);usleep(100000);
					return 0;
				}

				printf("LINE: %d\n",__LINE__);fflush(stdout);usleep(100000);
				argc = (int *)buffer;
				*argc = 0;

				printf("LINE: %d\n",__LINE__);fflush(stdout);usleep(100000);

				memcpy(buffer + 4, block, block_read_bytes);
			}
			else {
				printf("LINE: %d\n",__LINE__);fflush(stdout);usleep(100000);
				size = size + block_read_bytes;
				buffer = (char *)realloc(buffer, size);

				if(NULL == buffer) {
					return 0;
				}

				memcpy(buffer + 4 + read_bytes, block, block_read_bytes);
			}

			//buffer[0][size - 1] = 0; /* NULL terminate */

			read_bytes += block_read_bytes;

			for(i = 0; i < block_read_bytes; i++) {
				if (block[i] == 0) {
					printf("found argument null at %d\n",i);
					fflush(stdout);
					usleep(100000);
					*argc = *argc + 1;
				}
			}
		}
	} while(block_read_bytes > 0);

	printf("argc: %d\n", *argc);fflush(stdout);
	usleep(100000);

	return buffer;
#endif
}

void
x_cgo_sys_thread_create(void* (*func)(void*), void* arg) {
	pthread_t p;
	int err = _cgo_try_pthread_create(&p, NULL, func, arg);
	if (err != 0) {
		fprintf(stderr, "pthread_create failed: %s", strerror(err));
		abort();
	}
}

uintptr_t
_cgo_wait_runtime_init_done(void) {
	void (*pfn)(struct context_arg*);

	pthread_mutex_lock(&runtime_init_mu);
	while (runtime_init_done == 0) {
		pthread_cond_wait(&runtime_init_cond, &runtime_init_mu);
	}

	// TODO(iant): For the case of a new C thread calling into Go, such
	// as when using -buildmode=c-archive, we know that Go runtime
	// initialization is complete but we do not know that all Go init
	// functions have been run. We should not fetch cgo_context_function
	// until they have been, because that is where a call to
	// SetCgoTraceback is likely to occur. We are going to wait for Go
	// initialization to be complete anyhow, later, by waiting for
	// main_init_done to be closed in cgocallbackg1. We should wait here
	// instead. See also issue #15943.
	pfn = cgo_context_function;

	pthread_mutex_unlock(&runtime_init_mu);
	if (pfn != nil) {
		struct context_arg arg;

		arg.Context = 0;
		(*pfn)(&arg);
		return arg.Context;
	}
	return 0;
}

void
x_cgo_notify_runtime_init_done(void* dummy __attribute__ ((unused))) {
	pthread_mutex_lock(&runtime_init_mu);
	runtime_init_done = 1;
	pthread_cond_broadcast(&runtime_init_cond);
	pthread_mutex_unlock(&runtime_init_mu);
}

// Sets the context function to call to record the traceback context
// when calling a Go function from C code. Called from runtime.SetCgoTraceback.
void x_cgo_set_context_function(void (*context)(struct context_arg*)) {
	pthread_mutex_lock(&runtime_init_mu);
	cgo_context_function = context;
	pthread_mutex_unlock(&runtime_init_mu);
}

// Gets the context function.
void (*(_cgo_get_context_function(void)))(struct context_arg*) {
	void (*ret)(struct context_arg*);

	pthread_mutex_lock(&runtime_init_mu);
	ret = cgo_context_function;
	pthread_mutex_unlock(&runtime_init_mu);
	return ret;
}

// _cgo_try_pthread_create retries pthread_create if it fails with
// EAGAIN.
int
_cgo_try_pthread_create(pthread_t* thread, const pthread_attr_t* attr, void* (*pfn)(void*), void* arg) {
	int tries;
	int err;
	struct timespec ts;

	for (tries = 0; tries < 20; tries++) {
		err = pthread_create(thread, attr, pfn, arg);
		if (err == 0) {
			pthread_detach(*thread);
			return 0;
		}
		if (err != EAGAIN) {
			return err;
		}
		ts.tv_sec = 0;
		ts.tv_nsec = (tries + 1) * 1000 * 1000; // Milliseconds.
		nanosleep(&ts, nil);
	}
	return EAGAIN;
}
