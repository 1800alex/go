// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "go_tls.h"
#include "tls_arm64.h"
#include "funcdata.h"
#include "textflag.h"

TEXT runtime·rt0_go(SB),NOSPLIT|TOPFRAME,$0
	// SP = stack; R0 = argc; R1 = argv

	SUB	$32, RSP
	MOVW	R0, 8(RSP) // argc
	MOVD	R1, 16(RSP) // argv

#ifdef TLS_darwin
	// Initialize TLS.
	MOVD	ZR, g // clear g, make sure it's not junk.
	SUB	$32, RSP
	MRS_TPIDR_R0
	AND	$~7, R0
	MOVD	R0, 16(RSP)             // arg2: TLS base
	MOVD	$runtime·tls_g(SB), R2
	MOVD	R2, 8(RSP)              // arg1: &tlsg
	BL	·tlsinit(SB)
	ADD	$32, RSP
#endif

	// create istack out of the given (operating system) stack.
	// _cgo_init may update stackguard.
	MOVD	$runtime·g0(SB), g
	MOVD	RSP, R7
	MOVD	$(-64*1024)(R7), R0
	MOVD	R0, g_stackguard0(g)
	MOVD	R0, g_stackguard1(g)
	MOVD	R0, (g_stack+stack_lo)(g)
	MOVD	R7, (g_stack+stack_hi)(g)

	// if there is a _cgo_init, call it using the gcc ABI.
	MOVD	_cgo_init(SB), R12
	CBZ	R12, nocgo

#ifdef GOOS_android
	MRS_TPIDR_R0			// load TLS base pointer
	MOVD	R0, R3			// arg 3: TLS base pointer
	MOVD	$runtime·tls_g(SB), R2 	// arg 2: &tls_g
#else
	MOVD	$0, R2		        // arg 2: not used when using platform's TLS
#endif
	MOVD	$setg_gcc<>(SB), R1	// arg 1: setg
	MOVD	g, R0			// arg 0: G
	SUB	$16, RSP		// reserve 16 bytes for sp-8 where fp may be saved.
	BL	(R12)
	ADD	$16, RSP

nocgo:
	BL	runtime·save_g(SB)
	// update stackguard after _cgo_init
	MOVD	(g_stack+stack_lo)(g), R0
	ADD	$const__StackGuard, R0
	MOVD	R0, g_stackguard0(g)
	MOVD	R0, g_stackguard1(g)

	// set the per-goroutine and per-mach "registers"
	MOVD	$runtime·m0(SB), R0

	// save m->g0 = g0
	MOVD	g, m_g0(R0)
	// save m0 to g0->m
	MOVD	R0, g_m(g)

	BL	runtime·check(SB)

#ifdef GOOS_windows
	BL	runtime·wintls(SB)
#endif

	MOVW	8(RSP), R0	// copy argc
	MOVW	R0, -8(RSP)
	MOVD	16(RSP), R0		// copy argv
	MOVD	R0, 0(RSP)
	BL	runtime·args(SB)
	BL	runtime·osinit(SB)
	BL	runtime·schedinit(SB)

	// create a new goroutine to start program
	MOVD	$runtime·mainPC(SB), R0		// entry
	SUB	$16, RSP
	MOVD	R0, 8(RSP) // arg
	MOVD	$0, 0(RSP) // dummy LR
	BL	runtime·newproc(SB)
	ADD	$16, RSP

	// start this M
	BL	runtime·mstart(SB)

	MOVD	$0, R0
	MOVD	R0, (R0)	// boom
	UNDEF

DATA	runtime·mainPC+0(SB)/8,$runtime·main<ABIInternal>(SB)
GLOBL	runtime·mainPC(SB),RODATA,$8

TEXT runtime·breakpoint(SB),NOSPLIT|NOFRAME,$0-0
	BRK
	RET

TEXT runtime·asminit(SB),NOSPLIT|NOFRAME,$0-0
	RET

TEXT runtime·mstart(SB),NOSPLIT|TOPFRAME,$0
	BL	runtime·mstart0(SB)
	RET // not reached

/*
 *  go-routine
 */

// void gogo(Gobuf*)
// restore state from Gobuf; longjmp
TEXT runtime·gogo(SB), NOSPLIT|NOFRAME, $0-8
	MOVD	buf+0(FP), R5
	MOVD	gobuf_g(R5), R6
	MOVD	0(R6), R4	// make sure g != nil
	B	gogo<>(SB)

TEXT gogo<>(SB), NOSPLIT|NOFRAME, $0
	MOVD	R6, g
	BL	runtime·save_g(SB)

	MOVD	gobuf_sp(R5), R0
	MOVD	R0, RSP
	MOVD	gobuf_bp(R5), R29
	MOVD	gobuf_lr(R5), LR
	MOVD	gobuf_ret(R5), R0
	MOVD	gobuf_ctxt(R5), R26
	MOVD	$0, gobuf_sp(R5)
	MOVD	$0, gobuf_bp(R5)
	MOVD	$0, gobuf_ret(R5)
	MOVD	$0, gobuf_lr(R5)
	MOVD	$0, gobuf_ctxt(R5)
	CMP	ZR, ZR // set condition codes for == test, needed by stack split
	MOVD	gobuf_pc(R5), R6
	B	(R6)

// void mcall(fn func(*g))
// Switch to m->g0's stack, call fn(g).
// Fn must never return. It should gogo(&g->sched)
// to keep running g.
TEXT runtime·mcall<ABIInternal>(SB), NOSPLIT|NOFRAME, $0-8
	MOVD	R0, R26				// context

	// Save caller state in g->sched
	MOVD	RSP, R0
	MOVD	R0, (g_sched+gobuf_sp)(g)
	MOVD	R29, (g_sched+gobuf_bp)(g)
	MOVD	LR, (g_sched+gobuf_pc)(g)
	MOVD	$0, (g_sched+gobuf_lr)(g)

	// Switch to m->g0 & its stack, call fn.
	MOVD	g, R3
	MOVD	g_m(g), R8
	MOVD	m_g0(R8), g
	BL	runtime·save_g(SB)
	CMP	g, R3
	BNE	2(PC)
	B	runtime·badmcall(SB)

	MOVD	(g_sched+gobuf_sp)(g), R0
	MOVD	R0, RSP	// sp = m->g0->sched.sp
	MOVD	(g_sched+gobuf_bp)(g), R29
	MOVD	R3, R0				// arg = g
	MOVD	$0, -16(RSP)			// dummy LR
	SUB	$16, RSP
	MOVD	0(R26), R4			// code pointer
	BL	(R4)
	B	runtime·badmcall2(SB)

// systemstack_switch is a dummy routine that systemstack leaves at the bottom
// of the G stack. We need to distinguish the routine that
// lives at the bottom of the G stack from the one that lives
// at the top of the system stack because the one at the top of
// the system stack terminates the stack walk (see topofstack()).
TEXT runtime·systemstack_switch(SB), NOSPLIT, $0-0
	UNDEF
	BL	(LR)	// make sure this function is not leaf
	RET

// func systemstack(fn func())
TEXT runtime·systemstack(SB), NOSPLIT, $0-8
	MOVD	fn+0(FP), R3	// R3 = fn
	MOVD	R3, R26		// context
	MOVD	g_m(g), R4	// R4 = m

	MOVD	m_gsignal(R4), R5	// R5 = gsignal
	CMP	g, R5
	BEQ	noswitch

	MOVD	m_g0(R4), R5	// R5 = g0
	CMP	g, R5
	BEQ	noswitch

	MOVD	m_curg(R4), R6
	CMP	g, R6
	BEQ	switch

	// Bad: g is not gsignal, not g0, not curg. What is it?
	// Hide call from linker nosplit analysis.
	MOVD	$runtime·badsystemstack(SB), R3
	BL	(R3)
	B	runtime·abort(SB)

switch:
	// save our state in g->sched. Pretend to
	// be systemstack_switch if the G stack is scanned.
	BL	gosave_systemstack_switch<>(SB)

	// switch to g0
	MOVD	R5, g
	BL	runtime·save_g(SB)
	MOVD	(g_sched+gobuf_sp)(g), R3
	MOVD	R3, RSP
	MOVD	(g_sched+gobuf_bp)(g), R29

	// call target function
	MOVD	0(R26), R3	// code pointer
	BL	(R3)

	// switch back to g
	MOVD	g_m(g), R3
	MOVD	m_curg(R3), g
	BL	runtime·save_g(SB)
	MOVD	(g_sched+gobuf_sp)(g), R0
	MOVD	R0, RSP
	MOVD	(g_sched+gobuf_bp)(g), R29
	MOVD	$0, (g_sched+gobuf_sp)(g)
	MOVD	$0, (g_sched+gobuf_bp)(g)
	RET

noswitch:
	// already on m stack, just call directly
	// Using a tail call here cleans up tracebacks since we won't stop
	// at an intermediate systemstack.
	MOVD	0(R26), R3	// code pointer
	MOVD.P	16(RSP), R30	// restore LR
	SUB	$8, RSP, R29	// restore FP
	B	(R3)

/*
 * support for morestack
 */

// Called during function prolog when more stack is needed.
// Caller has already loaded:
// R3 prolog's LR (R30)
//
// The traceback routines see morestack on a g0 as being
// the top of a stack (for example, morestack calling newstack
// calling the scheduler calling newm calling gc), so we must
// record an argument size. For that purpose, it has no arguments.
TEXT runtime·morestack(SB),NOSPLIT|NOFRAME,$0-0
	// Cannot grow scheduler stack (m->g0).
	MOVD	g_m(g), R8
	MOVD	m_g0(R8), R4
	CMP	g, R4
	BNE	3(PC)
	BL	runtime·badmorestackg0(SB)
	B	runtime·abort(SB)

	// Cannot grow signal stack (m->gsignal).
	MOVD	m_gsignal(R8), R4
	CMP	g, R4
	BNE	3(PC)
	BL	runtime·badmorestackgsignal(SB)
	B	runtime·abort(SB)

	// Called from f.
	// Set g->sched to context in f
	MOVD	RSP, R0
	MOVD	R0, (g_sched+gobuf_sp)(g)
	MOVD	R29, (g_sched+gobuf_bp)(g)
	MOVD	LR, (g_sched+gobuf_pc)(g)
	MOVD	R3, (g_sched+gobuf_lr)(g)
	MOVD	R26, (g_sched+gobuf_ctxt)(g)

	// Called from f.
	// Set m->morebuf to f's callers.
	MOVD	R3, (m_morebuf+gobuf_pc)(R8)	// f's caller's PC
	MOVD	RSP, R0
	MOVD	R0, (m_morebuf+gobuf_sp)(R8)	// f's caller's RSP
	MOVD	g, (m_morebuf+gobuf_g)(R8)

	// Call newstack on m->g0's stack.
	MOVD	m_g0(R8), g
	BL	runtime·save_g(SB)
	MOVD	(g_sched+gobuf_sp)(g), R0
	MOVD	R0, RSP
	MOVD	(g_sched+gobuf_bp)(g), R29
	MOVD.W	$0, -16(RSP)	// create a call frame on g0 (saved LR; keep 16-aligned)
	BL	runtime·newstack(SB)

	// Not reached, but make sure the return PC from the call to newstack
	// is still in this function, and not the beginning of the next.
	UNDEF

TEXT runtime·morestack_noctxt(SB),NOSPLIT|NOFRAME,$0-0
	MOVW	$0, R26
	B runtime·morestack(SB)

// spillArgs stores return values from registers to a *internal/abi.RegArgs in R20.
TEXT ·spillArgs(SB),NOSPLIT,$0-0
	MOVD	R0, (0*8)(R20)
	MOVD	R1, (1*8)(R20)
	MOVD	R2, (2*8)(R20)
	MOVD	R3, (3*8)(R20)
	MOVD	R4, (4*8)(R20)
	MOVD	R5, (5*8)(R20)
	MOVD	R6, (6*8)(R20)
	MOVD	R7, (7*8)(R20)
	MOVD	R8, (8*8)(R20)
	MOVD	R9, (9*8)(R20)
	MOVD	R10, (10*8)(R20)
	MOVD	R11, (11*8)(R20)
	MOVD	R12, (12*8)(R20)
	MOVD	R13, (13*8)(R20)
	MOVD	R14, (14*8)(R20)
	MOVD	R15, (15*8)(R20)
	FMOVD	F0, (16*8)(R20)
	FMOVD	F1, (17*8)(R20)
	FMOVD	F2, (18*8)(R20)
	FMOVD	F3, (19*8)(R20)
	FMOVD	F4, (20*8)(R20)
	FMOVD	F5, (21*8)(R20)
	FMOVD	F6, (22*8)(R20)
	FMOVD	F7, (23*8)(R20)
	FMOVD	F8, (24*8)(R20)
	FMOVD	F9, (25*8)(R20)
	FMOVD	F10, (26*8)(R20)
	FMOVD	F11, (27*8)(R20)
	FMOVD	F12, (28*8)(R20)
	FMOVD	F13, (29*8)(R20)
	FMOVD	F14, (30*8)(R20)
	FMOVD	F15, (31*8)(R20)
	RET

// unspillArgs loads args into registers from a *internal/abi.RegArgs in R20.
TEXT ·unspillArgs(SB),NOSPLIT,$0-0
	MOVD	(0*8)(R20), R0
	MOVD	(1*8)(R20), R1
	MOVD	(2*8)(R20), R2
	MOVD	(3*8)(R20), R3
	MOVD	(4*8)(R20), R4
	MOVD	(5*8)(R20), R5
	MOVD	(6*8)(R20), R6
	MOVD	(7*8)(R20), R7
	MOVD	(8*8)(R20), R8
	MOVD	(9*8)(R20), R9
	MOVD	(10*8)(R20), R10
	MOVD	(11*8)(R20), R11
	MOVD	(12*8)(R20), R12
	MOVD	(13*8)(R20), R13
	MOVD	(14*8)(R20), R14
	MOVD	(15*8)(R20), R15
	FMOVD	(16*8)(R20), F0
	FMOVD	(17*8)(R20), F1
	FMOVD	(18*8)(R20), F2
	FMOVD	(19*8)(R20), F3
	FMOVD	(20*8)(R20), F4
	FMOVD	(21*8)(R20), F5
	FMOVD	(22*8)(R20), F6
	FMOVD	(23*8)(R20), F7
	FMOVD	(24*8)(R20), F8
	FMOVD	(25*8)(R20), F9
	FMOVD	(26*8)(R20), F10
	FMOVD	(27*8)(R20), F11
	FMOVD	(28*8)(R20), F12
	FMOVD	(29*8)(R20), F13
	FMOVD	(30*8)(R20), F14
	FMOVD	(31*8)(R20), F15
	RET

// reflectcall: call a function with the given argument list
// func call(stackArgsType *_type, f *FuncVal, stackArgs *byte, stackArgsSize, stackRetOffset, frameSize uint32, regArgs *abi.RegArgs).
// we don't have variable-sized frames, so we use a small number
// of constant-sized-frame functions to encode a few bits of size in the pc.
// Caution: ugly multiline assembly macros in your future!

#define DISPATCH(NAME,MAXSIZE)		\
	MOVD	$MAXSIZE, R27;		\
	CMP	R27, R16;		\
	BGT	3(PC);			\
	MOVD	$NAME(SB), R27;	\
	B	(R27)
// Note: can't just "B NAME(SB)" - bad inlining results.

TEXT ·reflectcall(SB), NOSPLIT|NOFRAME, $0-48
	MOVWU	frameSize+32(FP), R16
	DISPATCH(runtime·call16, 16)
	DISPATCH(runtime·call32, 32)
	DISPATCH(runtime·call64, 64)
	DISPATCH(runtime·call128, 128)
	DISPATCH(runtime·call256, 256)
	DISPATCH(runtime·call512, 512)
	DISPATCH(runtime·call1024, 1024)
	DISPATCH(runtime·call2048, 2048)
	DISPATCH(runtime·call4096, 4096)
	DISPATCH(runtime·call8192, 8192)
	DISPATCH(runtime·call16384, 16384)
	DISPATCH(runtime·call32768, 32768)
	DISPATCH(runtime·call65536, 65536)
	DISPATCH(runtime·call131072, 131072)
	DISPATCH(runtime·call262144, 262144)
	DISPATCH(runtime·call524288, 524288)
	DISPATCH(runtime·call1048576, 1048576)
	DISPATCH(runtime·call2097152, 2097152)
	DISPATCH(runtime·call4194304, 4194304)
	DISPATCH(runtime·call8388608, 8388608)
	DISPATCH(runtime·call16777216, 16777216)
	DISPATCH(runtime·call33554432, 33554432)
	DISPATCH(runtime·call67108864, 67108864)
	DISPATCH(runtime·call134217728, 134217728)
	DISPATCH(runtime·call268435456, 268435456)
	DISPATCH(runtime·call536870912, 536870912)
	DISPATCH(runtime·call1073741824, 1073741824)
	MOVD	$runtime·badreflectcall(SB), R0
	B	(R0)

#define CALLFN(NAME,MAXSIZE)			\
TEXT NAME(SB), WRAPPER, $MAXSIZE-48;		\
	NO_LOCAL_POINTERS;			\
	/* copy arguments to stack */		\
	MOVD	stackArgs+16(FP), R3;			\
	MOVWU	stackArgsSize+24(FP), R4;		\
	ADD	$8, RSP, R5;			\
	BIC	$0xf, R4, R6;			\
	CBZ	R6, 6(PC);			\
	/* if R6=(argsize&~15) != 0 */		\
	ADD	R6, R5, R6;			\
	/* copy 16 bytes a time */		\
	LDP.P	16(R3), (R7, R8);		\
	STP.P	(R7, R8), 16(R5);		\
	CMP	R5, R6;				\
	BNE	-3(PC);				\
	AND	$0xf, R4, R6;			\
	CBZ	R6, 6(PC);			\
	/* if R6=(argsize&15) != 0 */		\
	ADD	R6, R5, R6;			\
	/* copy 1 byte a time for the rest */	\
	MOVBU.P	1(R3), R7;			\
	MOVBU.P	R7, 1(R5);			\
	CMP	R5, R6;				\
	BNE	-3(PC);				\
	/* set up argument registers */		\
	MOVD	regArgs+40(FP), R20;		\
	CALL	·unspillArgs(SB);		\
	/* call function */			\
	MOVD	f+8(FP), R26;			\
	MOVD	(R26), R20;			\
	PCDATA	$PCDATA_StackMapIndex, $0;	\
	BL	(R20);				\
	/* copy return values back */		\
	MOVD	regArgs+40(FP), R20;		\
	CALL	·spillArgs(SB);		\
	MOVD	stackArgsType+0(FP), R7;		\
	MOVD	stackArgs+16(FP), R3;			\
	MOVWU	stackArgsSize+24(FP), R4;			\
	MOVWU	stackRetOffset+28(FP), R6;		\
	ADD	$8, RSP, R5;			\
	ADD	R6, R5; 			\
	ADD	R6, R3;				\
	SUB	R6, R4;				\
	BL	callRet<>(SB);			\
	RET

// callRet copies return values back at the end of call*. This is a
// separate function so it can allocate stack space for the arguments
// to reflectcallmove. It does not follow the Go ABI; it expects its
// arguments in registers.
TEXT callRet<>(SB), NOSPLIT, $48-0
	NO_LOCAL_POINTERS
	MOVD	R7, 8(RSP)
	MOVD	R3, 16(RSP)
	MOVD	R5, 24(RSP)
	MOVD	R4, 32(RSP)
	MOVD	R20, 40(RSP)
	BL	runtime·reflectcallmove(SB)
	RET

CALLFN(·call16, 16)
CALLFN(·call32, 32)
CALLFN(·call64, 64)
CALLFN(·call128, 128)
CALLFN(·call256, 256)
CALLFN(·call512, 512)
CALLFN(·call1024, 1024)
CALLFN(·call2048, 2048)
CALLFN(·call4096, 4096)
CALLFN(·call8192, 8192)
CALLFN(·call16384, 16384)
CALLFN(·call32768, 32768)
CALLFN(·call65536, 65536)
CALLFN(·call131072, 131072)
CALLFN(·call262144, 262144)
CALLFN(·call524288, 524288)
CALLFN(·call1048576, 1048576)
CALLFN(·call2097152, 2097152)
CALLFN(·call4194304, 4194304)
CALLFN(·call8388608, 8388608)
CALLFN(·call16777216, 16777216)
CALLFN(·call33554432, 33554432)
CALLFN(·call67108864, 67108864)
CALLFN(·call134217728, 134217728)
CALLFN(·call268435456, 268435456)
CALLFN(·call536870912, 536870912)
CALLFN(·call1073741824, 1073741824)

// func memhash32(p unsafe.Pointer, h uintptr) uintptr
TEXT runtime·memhash32<ABIInternal>(SB),NOSPLIT|NOFRAME,$0-24
	MOVB	runtime·useAeshash(SB), R10
	CBZ	R10, noaes
	MOVD	$runtime·aeskeysched+0(SB), R3

	VEOR	V0.B16, V0.B16, V0.B16
	VLD1	(R3), [V2.B16]
	VLD1	(R0), V0.S[1]
	VMOV	R1, V0.S[0]

	AESE	V2.B16, V0.B16
	AESMC	V0.B16, V0.B16
	AESE	V2.B16, V0.B16
	AESMC	V0.B16, V0.B16
	AESE	V2.B16, V0.B16

	VMOV	V0.D[0], R0
	RET
noaes:
	B	runtime·memhash32Fallback<ABIInternal>(SB)

// func memhash64(p unsafe.Pointer, h uintptr) uintptr
TEXT runtime·memhash64<ABIInternal>(SB),NOSPLIT|NOFRAME,$0-24
	MOVB	runtime·useAeshash(SB), R10
	CBZ	R10, noaes
	MOVD	$runtime·aeskeysched+0(SB), R3

	VEOR	V0.B16, V0.B16, V0.B16
	VLD1	(R3), [V2.B16]
	VLD1	(R0), V0.D[1]
	VMOV	R1, V0.D[0]

	AESE	V2.B16, V0.B16
	AESMC	V0.B16, V0.B16
	AESE	V2.B16, V0.B16
	AESMC	V0.B16, V0.B16
	AESE	V2.B16, V0.B16

	VMOV	V0.D[0], R0
	RET
noaes:
	B	runtime·memhash64Fallback<ABIInternal>(SB)

// func memhash(p unsafe.Pointer, h, size uintptr) uintptr
TEXT runtime·memhash<ABIInternal>(SB),NOSPLIT|NOFRAME,$0-32
	MOVB	runtime·useAeshash(SB), R10
	CBZ	R10, noaes
	B	aeshashbody<>(SB)
noaes:
	B	runtime·memhashFallback<ABIInternal>(SB)

// func strhash(p unsafe.Pointer, h uintptr) uintptr
TEXT runtime·strhash<ABIInternal>(SB),NOSPLIT|NOFRAME,$0-24
	MOVB	runtime·useAeshash(SB), R10
	CBZ	R10, noaes
	LDP	(R0), (R0, R2)	// string data / length
	B	aeshashbody<>(SB)
noaes:
	B	runtime·strhashFallback<ABIInternal>(SB)

// R0: data
// R1: seed data
// R2: length
// At return, R0 = return value
TEXT aeshashbody<>(SB),NOSPLIT|NOFRAME,$0
	VEOR	V30.B16, V30.B16, V30.B16
	VMOV	R1, V30.D[0]
	VMOV	R2, V30.D[1] // load length into seed

	MOVD	$runtime·aeskeysched+0(SB), R4
	VLD1.P	16(R4), [V0.B16]
	AESE	V30.B16, V0.B16
	AESMC	V0.B16, V0.B16
	CMP	$16, R2
	BLO	aes0to15
	BEQ	aes16
	CMP	$32, R2
	BLS	aes17to32
	CMP	$64, R2
	BLS	aes33to64
	CMP	$128, R2
	BLS	aes65to128
	B	aes129plus

aes0to15:
	CBZ	R2, aes0
	VEOR	V2.B16, V2.B16, V2.B16
	TBZ	$3, R2, less_than_8
	VLD1.P	8(R0), V2.D[0]

less_than_8:
	TBZ	$2, R2, less_than_4
	VLD1.P	4(R0), V2.S[2]

less_than_4:
	TBZ	$1, R2, less_than_2
	VLD1.P	2(R0), V2.H[6]

less_than_2:
	TBZ	$0, R2, done
	VLD1	(R0), V2.B[14]
done:
	AESE	V0.B16, V2.B16
	AESMC	V2.B16, V2.B16
	AESE	V0.B16, V2.B16
	AESMC	V2.B16, V2.B16
	AESE	V0.B16, V2.B16

	VMOV	V2.D[0], R0
	RET

aes0:
	VMOV	V0.D[0], R0
	RET

aes16:
	VLD1	(R0), [V2.B16]
	B	done

aes17to32:
	// make second seed
	VLD1	(R4), [V1.B16]
	AESE	V30.B16, V1.B16
	AESMC	V1.B16, V1.B16
	SUB	$16, R2, R10
	VLD1.P	(R0)(R10), [V2.B16]
	VLD1	(R0), [V3.B16]

	AESE	V0.B16, V2.B16
	AESMC	V2.B16, V2.B16
	AESE	V1.B16, V3.B16
	AESMC	V3.B16, V3.B16

	AESE	V0.B16, V2.B16
	AESMC	V2.B16, V2.B16
	AESE	V1.B16, V3.B16
	AESMC	V3.B16, V3.B16

	AESE	V0.B16, V2.B16
	AESE	V1.B16, V3.B16

	VEOR	V3.B16, V2.B16, V2.B16

	VMOV	V2.D[0], R0
	RET

aes33to64:
	VLD1	(R4), [V1.B16, V2.B16, V3.B16]
	AESE	V30.B16, V1.B16
	AESMC	V1.B16, V1.B16
	AESE	V30.B16, V2.B16
	AESMC	V2.B16, V2.B16
	AESE	V30.B16, V3.B16
	AESMC	V3.B16, V3.B16
	SUB	$32, R2, R10

	VLD1.P	(R0)(R10), [V4.B16, V5.B16]
	VLD1	(R0), [V6.B16, V7.B16]

	AESE	V0.B16, V4.B16
	AESMC	V4.B16, V4.B16
	AESE	V1.B16, V5.B16
	AESMC	V5.B16, V5.B16
	AESE	V2.B16, V6.B16
	AESMC	V6.B16, V6.B16
	AESE	V3.B16, V7.B16
	AESMC	V7.B16, V7.B16

	AESE	V0.B16, V4.B16
	AESMC	V4.B16, V4.B16
	AESE	V1.B16, V5.B16
	AESMC	V5.B16, V5.B16
	AESE	V2.B16, V6.B16
	AESMC	V6.B16, V6.B16
	AESE	V3.B16, V7.B16
	AESMC	V7.B16, V7.B16

	AESE	V0.B16, V4.B16
	AESE	V1.B16, V5.B16
	AESE	V2.B16, V6.B16
	AESE	V3.B16, V7.B16

	VEOR	V6.B16, V4.B16, V4.B16
	VEOR	V7.B16, V5.B16, V5.B16
	VEOR	V5.B16, V4.B16, V4.B16

	VMOV	V4.D[0], R0
	RET

aes65to128:
	VLD1.P	64(R4), [V1.B16, V2.B16, V3.B16, V4.B16]
	VLD1	(R4), [V5.B16, V6.B16, V7.B16]
	AESE	V30.B16, V1.B16
	AESMC	V1.B16, V1.B16
	AESE	V30.B16, V2.B16
	AESMC	V2.B16, V2.B16
	AESE	V30.B16, V3.B16
	AESMC	V3.B16, V3.B16
	AESE	V30.B16, V4.B16
	AESMC	V4.B16, V4.B16
	AESE	V30.B16, V5.B16
	AESMC	V5.B16, V5.B16
	AESE	V30.B16, V6.B16
	AESMC	V6.B16, V6.B16
	AESE	V30.B16, V7.B16
	AESMC	V7.B16, V7.B16

	SUB	$64, R2, R10
	VLD1.P	(R0)(R10), [V8.B16, V9.B16, V10.B16, V11.B16]
	VLD1	(R0), [V12.B16, V13.B16, V14.B16, V15.B16]
	AESE	V0.B16,	 V8.B16
	AESMC	V8.B16,  V8.B16
	AESE	V1.B16,	 V9.B16
	AESMC	V9.B16,  V9.B16
	AESE	V2.B16, V10.B16
	AESMC	V10.B16,  V10.B16
	AESE	V3.B16, V11.B16
	AESMC	V11.B16,  V11.B16
	AESE	V4.B16, V12.B16
	AESMC	V12.B16,  V12.B16
	AESE	V5.B16, V13.B16
	AESMC	V13.B16,  V13.B16
	AESE	V6.B16, V14.B16
	AESMC	V14.B16,  V14.B16
	AESE	V7.B16, V15.B16
	AESMC	V15.B16,  V15.B16

	AESE	V0.B16,	 V8.B16
	AESMC	V8.B16,  V8.B16
	AESE	V1.B16,	 V9.B16
	AESMC	V9.B16,  V9.B16
	AESE	V2.B16, V10.B16
	AESMC	V10.B16,  V10.B16
	AESE	V3.B16, V11.B16
	AESMC	V11.B16,  V11.B16
	AESE	V4.B16, V12.B16
	AESMC	V12.B16,  V12.B16
	AESE	V5.B16, V13.B16
	AESMC	V13.B16,  V13.B16
	AESE	V6.B16, V14.B16
	AESMC	V14.B16,  V14.B16
	AESE	V7.B16, V15.B16
	AESMC	V15.B16,  V15.B16

	AESE	V0.B16,	 V8.B16
	AESE	V1.B16,	 V9.B16
	AESE	V2.B16, V10.B16
	AESE	V3.B16, V11.B16
	AESE	V4.B16, V12.B16
	AESE	V5.B16, V13.B16
	AESE	V6.B16, V14.B16
	AESE	V7.B16, V15.B16

	VEOR	V12.B16, V8.B16, V8.B16
	VEOR	V13.B16, V9.B16, V9.B16
	VEOR	V14.B16, V10.B16, V10.B16
	VEOR	V15.B16, V11.B16, V11.B16
	VEOR	V10.B16, V8.B16, V8.B16
	VEOR	V11.B16, V9.B16, V9.B16
	VEOR	V9.B16, V8.B16, V8.B16

	VMOV	V8.D[0], R0
	RET

aes129plus:
	PRFM (R0), PLDL1KEEP
	VLD1.P	64(R4), [V1.B16, V2.B16, V3.B16, V4.B16]
	VLD1	(R4), [V5.B16, V6.B16, V7.B16]
	AESE	V30.B16, V1.B16
	AESMC	V1.B16, V1.B16
	AESE	V30.B16, V2.B16
	AESMC	V2.B16, V2.B16
	AESE	V30.B16, V3.B16
	AESMC	V3.B16, V3.B16
	AESE	V30.B16, V4.B16
	AESMC	V4.B16, V4.B16
	AESE	V30.B16, V5.B16
	AESMC	V5.B16, V5.B16
	AESE	V30.B16, V6.B16
	AESMC	V6.B16, V6.B16
	AESE	V30.B16, V7.B16
	AESMC	V7.B16, V7.B16
	ADD	R0, R2, R10
	SUB	$128, R10, R10
	VLD1.P	64(R10), [V8.B16, V9.B16, V10.B16, V11.B16]
	VLD1	(R10), [V12.B16, V13.B16, V14.B16, V15.B16]
	SUB	$1, R2, R2
	LSR	$7, R2, R2

aesloop:
	AESE	V8.B16,	 V0.B16
	AESMC	V0.B16,  V0.B16
	AESE	V9.B16,	 V1.B16
	AESMC	V1.B16,  V1.B16
	AESE	V10.B16, V2.B16
	AESMC	V2.B16,  V2.B16
	AESE	V11.B16, V3.B16
	AESMC	V3.B16,  V3.B16
	AESE	V12.B16, V4.B16
	AESMC	V4.B16,  V4.B16
	AESE	V13.B16, V5.B16
	AESMC	V5.B16,  V5.B16
	AESE	V14.B16, V6.B16
	AESMC	V6.B16,  V6.B16
	AESE	V15.B16, V7.B16
	AESMC	V7.B16,  V7.B16

	VLD1.P	64(R0), [V8.B16, V9.B16, V10.B16, V11.B16]
	AESE	V8.B16,	 V0.B16
	AESMC	V0.B16,  V0.B16
	AESE	V9.B16,	 V1.B16
	AESMC	V1.B16,  V1.B16
	AESE	V10.B16, V2.B16
	AESMC	V2.B16,  V2.B16
	AESE	V11.B16, V3.B16
	AESMC	V3.B16,  V3.B16

	VLD1.P	64(R0), [V12.B16, V13.B16, V14.B16, V15.B16]
	AESE	V12.B16, V4.B16
	AESMC	V4.B16,  V4.B16
	AESE	V13.B16, V5.B16
	AESMC	V5.B16,  V5.B16
	AESE	V14.B16, V6.B16
	AESMC	V6.B16,  V6.B16
	AESE	V15.B16, V7.B16
	AESMC	V7.B16,  V7.B16
	SUB	$1, R2, R2
	CBNZ	R2, aesloop

	AESE	V8.B16,	 V0.B16
	AESMC	V0.B16,  V0.B16
	AESE	V9.B16,	 V1.B16
	AESMC	V1.B16,  V1.B16
	AESE	V10.B16, V2.B16
	AESMC	V2.B16,  V2.B16
	AESE	V11.B16, V3.B16
	AESMC	V3.B16,  V3.B16
	AESE	V12.B16, V4.B16
	AESMC	V4.B16,  V4.B16
	AESE	V13.B16, V5.B16
	AESMC	V5.B16,  V5.B16
	AESE	V14.B16, V6.B16
	AESMC	V6.B16,  V6.B16
	AESE	V15.B16, V7.B16
	AESMC	V7.B16,  V7.B16

	AESE	V8.B16,	 V0.B16
	AESMC	V0.B16,  V0.B16
	AESE	V9.B16,	 V1.B16
	AESMC	V1.B16,  V1.B16
	AESE	V10.B16, V2.B16
	AESMC	V2.B16,  V2.B16
	AESE	V11.B16, V3.B16
	AESMC	V3.B16,  V3.B16
	AESE	V12.B16, V4.B16
	AESMC	V4.B16,  V4.B16
	AESE	V13.B16, V5.B16
	AESMC	V5.B16,  V5.B16
	AESE	V14.B16, V6.B16
	AESMC	V6.B16,  V6.B16
	AESE	V15.B16, V7.B16
	AESMC	V7.B16,  V7.B16

	AESE	V8.B16,	 V0.B16
	AESE	V9.B16,	 V1.B16
	AESE	V10.B16, V2.B16
	AESE	V11.B16, V3.B16
	AESE	V12.B16, V4.B16
	AESE	V13.B16, V5.B16
	AESE	V14.B16, V6.B16
	AESE	V15.B16, V7.B16

	VEOR	V0.B16, V1.B16, V0.B16
	VEOR	V2.B16, V3.B16, V2.B16
	VEOR	V4.B16, V5.B16, V4.B16
	VEOR	V6.B16, V7.B16, V6.B16
	VEOR	V0.B16, V2.B16, V0.B16
	VEOR	V4.B16, V6.B16, V4.B16
	VEOR	V4.B16, V0.B16, V0.B16

	VMOV	V0.D[0], R0
	RET

TEXT runtime·procyield(SB),NOSPLIT,$0-0
	MOVWU	cycles+0(FP), R0
again:
	YIELD
	SUBW	$1, R0
	CBNZ	R0, again
	RET

// Save state of caller into g->sched,
// but using fake PC from systemstack_switch.
// Must only be called from functions with no locals ($0)
// or else unwinding from systemstack_switch is incorrect.
// Smashes R0.
TEXT gosave_systemstack_switch<>(SB),NOSPLIT|NOFRAME,$0
	MOVD	$runtime·systemstack_switch(SB), R0
	ADD	$8, R0	// get past prologue
	MOVD	R0, (g_sched+gobuf_pc)(g)
	MOVD	RSP, R0
	MOVD	R0, (g_sched+gobuf_sp)(g)
	MOVD	R29, (g_sched+gobuf_bp)(g)
	MOVD	$0, (g_sched+gobuf_lr)(g)
	MOVD	$0, (g_sched+gobuf_ret)(g)
	// Assert ctxt is zero. See func save.
	MOVD	(g_sched+gobuf_ctxt)(g), R0
	CBZ	R0, 2(PC)
	CALL	runtime·abort(SB)
	RET

// func asmcgocall_no_g(fn, arg unsafe.Pointer)
// Call fn(arg) aligned appropriately for the gcc ABI.
// Called on a system stack, and there may be no g yet (during needm).
TEXT ·asmcgocall_no_g(SB),NOSPLIT,$0-16
	MOVD	fn+0(FP), R1
	MOVD	arg+8(FP), R0
	SUB	$16, RSP	// skip over saved frame pointer below RSP
	BL	(R1)
	ADD	$16, RSP	// skip over saved frame pointer below RSP
	RET

// func asmcgocall(fn, arg unsafe.Pointer) int32
// Call fn(arg) on the scheduler stack,
// aligned appropriately for the gcc ABI.
// See cgocall.go for more details.
TEXT ·asmcgocall(SB),NOSPLIT,$0-20
	MOVD	fn+0(FP), R1
	MOVD	arg+8(FP), R0

	MOVD	RSP, R2		// save original stack pointer
	CBZ	g, nosave
	MOVD	g, R4

	// Figure out if we need to switch to m->g0 stack.
	// We get called to create new OS threads too, and those
	// come in on the m->g0 stack already. Or we might already
	// be on the m->gsignal stack.
	MOVD	g_m(g), R8
	MOVD	m_gsignal(R8), R3
	CMP	R3, g
	BEQ	nosave
	MOVD	m_g0(R8), R3
	CMP	R3, g
	BEQ	nosave

	// Switch to system stack.
	MOVD	R0, R9	// gosave_systemstack_switch<> and save_g might clobber R0
	BL	gosave_systemstack_switch<>(SB)
	MOVD	R3, g
	BL	runtime·save_g(SB)
	MOVD	(g_sched+gobuf_sp)(g), R0
	MOVD	R0, RSP
	MOVD	(g_sched+gobuf_bp)(g), R29
	MOVD	R9, R0

	// Now on a scheduling stack (a pthread-created stack).
	// Save room for two of our pointers /*, plus 32 bytes of callee
	// save area that lives on the caller stack. */
	MOVD	RSP, R13
	SUB	$16, R13
	MOVD	R13, RSP
	MOVD	R4, 0(RSP)	// save old g on stack
	MOVD	(g_stack+stack_hi)(R4), R4
	SUB	R2, R4
	MOVD	R4, 8(RSP)	// save depth in old g stack (can't just save SP, as stack might be copied during a callback)
	BL	(R1)
	MOVD	R0, R9

	// Restore g, stack pointer. R0 is errno, so don't touch it
	MOVD	0(RSP), g
	BL	runtime·save_g(SB)
	MOVD	(g_stack+stack_hi)(g), R5
	MOVD	8(RSP), R6
	SUB	R6, R5
	MOVD	R9, R0
	MOVD	R5, RSP

	MOVW	R0, ret+16(FP)
	RET

nosave:
	// Running on a system stack, perhaps even without a g.
	// Having no g can happen during thread creation or thread teardown
	// (see needm/dropm on Solaris, for example).
	// This code is like the above sequence but without saving/restoring g
	// and without worrying about the stack moving out from under us
	// (because we're on a system stack, not a goroutine stack).
	// The above code could be used directly if already on a system stack,
	// but then the only path through this code would be a rare case on Solaris.
	// Using this code for all "already on system stack" calls exercises it more,
	// which should help keep it correct.
	MOVD	RSP, R13
	SUB	$16, R13
	MOVD	R13, RSP
	MOVD	$0, R4
	MOVD	R4, 0(RSP)	// Where above code stores g, in case someone looks during debugging.
	MOVD	R2, 8(RSP)	// Save original stack pointer.
	BL	(R1)
	// Restore stack pointer.
	MOVD	8(RSP), R2
	MOVD	R2, RSP
	MOVD	R0, ret+16(FP)
	RET

// cgocallback(fn, frame unsafe.Pointer, ctxt uintptr)
// See cgocall.go for more details.
TEXT ·cgocallback(SB),NOSPLIT,$24-24
	NO_LOCAL_POINTERS

	// Load g from thread-local storage.
	BL	runtime·load_g(SB)

	// If g is nil, Go did not create the current thread.
	// Call needm to obtain one for temporary use.
	// In this case, we're running on the thread stack, so there's
	// lots of space, but the linker doesn't know. Hide the call from
	// the linker analysis by using an indirect call.
	CBZ	g, needm

	MOVD	g_m(g), R8
	MOVD	R8, savedm-8(SP)
	B	havem

needm:
	MOVD	g, savedm-8(SP) // g is zero, so is m.
	MOVD	$runtime·needm(SB), R0
	BL	(R0)

	// Set m->g0->sched.sp = SP, so that if a panic happens
	// during the function we are about to execute, it will
	// have a valid SP to run on the g0 stack.
	// The next few lines (after the havem label)
	// will save this SP onto the stack and then write
	// the same SP back to m->sched.sp. That seems redundant,
	// but if an unrecovered panic happens, unwindm will
	// restore the g->sched.sp from the stack location
	// and then systemstack will try to use it. If we don't set it here,
	// that restored SP will be uninitialized (typically 0) and
	// will not be usable.
	MOVD	g_m(g), R8
	MOVD	m_g0(R8), R3
	MOVD	RSP, R0
	MOVD	R0, (g_sched+gobuf_sp)(R3)
	MOVD	R29, (g_sched+gobuf_bp)(R3)

havem:
	// Now there's a valid m, and we're running on its m->g0.
	// Save current m->g0->sched.sp on stack and then set it to SP.
	// Save current sp in m->g0->sched.sp in preparation for
	// switch back to m->curg stack.
	// NOTE: unwindm knows that the saved g->sched.sp is at 16(RSP) aka savedsp-16(SP).
	// Beware that the frame size is actually 32+16.
	MOVD	m_g0(R8), R3
	MOVD	(g_sched+gobuf_sp)(R3), R4
	MOVD	R4, savedsp-16(SP)
	MOVD	RSP, R0
	MOVD	R0, (g_sched+gobuf_sp)(R3)

	// Switch to m->curg stack and call runtime.cgocallbackg.
	// Because we are taking over the execution of m->curg
	// but *not* resuming what had been running, we need to
	// save that information (m->curg->sched) so we can restore it.
	// We can restore m->curg->sched.sp easily, because calling
	// runtime.cgocallbackg leaves SP unchanged upon return.
	// To save m->curg->sched.pc, we push it onto the curg stack and
	// open a frame the same size as cgocallback's g0 frame.
	// Once we switch to the curg stack, the pushed PC will appear
	// to be the return PC of cgocallback, so that the traceback
	// will seamlessly trace back into the earlier calls.
	MOVD	m_curg(R8), g
	BL	runtime·save_g(SB)
	MOVD	(g_sched+gobuf_sp)(g), R4 // prepare stack as R4
	MOVD	(g_sched+gobuf_pc)(g), R5
	MOVD	R5, -48(R4)
	MOVD	(g_sched+gobuf_bp)(g), R5
	MOVD	R5, -56(R4)
	// Gather our arguments into registers.
	MOVD	fn+0(FP), R1
	MOVD	frame+8(FP), R2
	MOVD	ctxt+16(FP), R3
	MOVD	$-48(R4), R0 // maintain 16-byte SP alignment
	MOVD	R0, RSP	// switch stack
	MOVD	R1, 8(RSP)
	MOVD	R2, 16(RSP)
	MOVD	R3, 24(RSP)
	MOVD	$runtime·cgocallbackg(SB), R0
	CALL	(R0) // indirect call to bypass nosplit check. We're on a different stack now.

	// Restore g->sched (== m->curg->sched) from saved values.
	MOVD	0(RSP), R5
	MOVD	R5, (g_sched+gobuf_pc)(g)
	MOVD	RSP, R4
	ADD	$48, R4, R4
	MOVD	R4, (g_sched+gobuf_sp)(g)

	// Switch back to m->g0's stack and restore m->g0->sched.sp.
	// (Unlike m->curg, the g0 goroutine never uses sched.pc,
	// so we do not have to restore it.)
	MOVD	g_m(g), R8
	MOVD	m_g0(R8), g
	BL	runtime·save_g(SB)
	MOVD	(g_sched+gobuf_sp)(g), R0
	MOVD	R0, RSP
	MOVD	savedsp-16(SP), R4
	MOVD	R4, (g_sched+gobuf_sp)(g)

	// If the m on entry was nil, we called needm above to borrow an m
	// for the duration of the call. Since the call is over, return it with dropm.
	MOVD	savedm-8(SP), R6
	CBNZ	R6, droppedm
	MOVD	$runtime·dropm(SB), R0
	BL	(R0)
droppedm:

	// Done!
	RET

// Called from cgo wrappers, this function returns g->m->curg.stack.hi.
// Must obey the gcc calling convention.
TEXT _cgo_topofstack(SB),NOSPLIT,$24
	// g (R28) and REGTMP (R27)  might be clobbered by load_g. They
	// are callee-save in the gcc calling convention, so save them.
	MOVD	R27, savedR27-8(SP)
	MOVD	g, saveG-16(SP)

	BL	runtime·load_g(SB)
	MOVD	g_m(g), R0
	MOVD	m_curg(R0), R0
	MOVD	(g_stack+stack_hi)(R0), R0

	MOVD	saveG-16(SP), g
	MOVD	savedR28-8(SP), R27
	RET

// void setg(G*); set g. for use by needm.
TEXT runtime·setg(SB), NOSPLIT, $0-8
	MOVD	gg+0(FP), g
	// This only happens if iscgo, so jump straight to save_g
	BL	runtime·save_g(SB)
	RET

// void setg_gcc(G*); set g called from gcc
TEXT setg_gcc<>(SB),NOSPLIT,$8
	MOVD	R0, g
	MOVD	R27, savedR27-8(SP)
	BL	runtime·save_g(SB)
	MOVD	savedR27-8(SP), R27
	RET

TEXT runtime·emptyfunc(SB),0,$0-0
	RET

TEXT runtime·abort(SB),NOSPLIT|NOFRAME,$0-0
	MOVD	ZR, R0
	MOVD	(R0), R0
	UNDEF

TEXT runtime·return0(SB), NOSPLIT, $0
	MOVW	$0, R0
	RET

// The top-most function running on a goroutine
// returns to goexit+PCQuantum.
TEXT runtime·goexit(SB),NOSPLIT|NOFRAME|TOPFRAME,$0-0
	MOVD	R0, R0	// NOP
	BL	runtime·goexit1(SB)	// does not return

// This is called from .init_array and follows the platform, not Go, ABI.
TEXT runtime·addmoduledata(SB),NOSPLIT,$0-0
	SUB	$0x10, RSP
	MOVD	R27, 8(RSP) // The access to global variables below implicitly uses R27, which is callee-save
	MOVD	runtime·lastmoduledatap(SB), R1
	MOVD	R0, moduledata_next(R1)
	MOVD	R0, runtime·lastmoduledatap(SB)
	MOVD	8(RSP), R27
	ADD	$0x10, RSP
	RET

TEXT ·checkASM(SB),NOSPLIT,$0-1
	MOVW	$1, R3
	MOVB	R3, ret+0(FP)
	RET

// gcWriteBarrier performs a heap pointer write and informs the GC.
//
// gcWriteBarrier does NOT follow the Go ABI. It takes two arguments:
// - R2 is the destination of the write
// - R3 is the value being written at R2
// It clobbers condition codes.
// It does not clobber any general-purpose registers,
// but may clobber others (e.g., floating point registers)
// The act of CALLing gcWriteBarrier will clobber R30 (LR).
//
// Defined as ABIInternal since the compiler generates ABIInternal
// calls to it directly and it does not use the stack-based Go ABI.
TEXT runtime·gcWriteBarrier<ABIInternal>(SB),NOSPLIT,$200
	// Save the registers clobbered by the fast path.
	MOVD	R0, 184(RSP)
	MOVD	R1, 192(RSP)
	MOVD	g_m(g), R0
	MOVD	m_p(R0), R0
	MOVD	(p_wbBuf+wbBuf_next)(R0), R1
	// Increment wbBuf.next position.
	ADD	$16, R1
	MOVD	R1, (p_wbBuf+wbBuf_next)(R0)
	MOVD	(p_wbBuf+wbBuf_end)(R0), R0
	CMP	R1, R0
	// Record the write.
	MOVD	R3, -16(R1)	// Record value
	MOVD	(R2), R0	// TODO: This turns bad writes into bad reads.
	MOVD	R0, -8(R1)	// Record *slot
	// Is the buffer full? (flags set in CMP above)
	BEQ	flush
ret:
	MOVD	184(RSP), R0
	MOVD	192(RSP), R1
	// Do the write.
	MOVD	R3, (R2)
	RET

flush:
	// Save all general purpose registers since these could be
	// clobbered by wbBufFlush and were not saved by the caller.
	MOVD	R2, 8(RSP)	// Also first argument to wbBufFlush
	MOVD	R3, 16(RSP)	// Also second argument to wbBufFlush
	// R0 already saved
	// R1 already saved
	MOVD	R4, 24(RSP)
	MOVD	R5, 32(RSP)
	MOVD	R6, 40(RSP)
	MOVD	R7, 48(RSP)
	MOVD	R8, 56(RSP)
	MOVD	R9, 64(RSP)
	MOVD	R10, 72(RSP)
	MOVD	R11, 80(RSP)
	MOVD	R12, 88(RSP)
	MOVD	R13, 96(RSP)
	MOVD	R14, 104(RSP)
	MOVD	R15, 112(RSP)
	// R16, R17 may be clobbered by linker trampoline
	// R18 is unused.
	MOVD	R19, 120(RSP)
	MOVD	R20, 128(RSP)
	MOVD	R21, 136(RSP)
	MOVD	R22, 144(RSP)
	MOVD	R23, 152(RSP)
	MOVD	R24, 160(RSP)
	MOVD	R25, 168(RSP)
	MOVD	R26, 176(RSP)
	// R27 is temp register.
	// R28 is g.
	// R29 is frame pointer (unused).
	// R30 is LR, which was saved by the prologue.
	// R31 is SP.

	// This takes arguments R2 and R3.
	CALL	runtime·wbBufFlush(SB)

	MOVD	8(RSP), R2
	MOVD	16(RSP), R3
	MOVD	24(RSP), R4
	MOVD	32(RSP), R5
	MOVD	40(RSP), R6
	MOVD	48(RSP), R7
	MOVD	56(RSP), R8
	MOVD	64(RSP), R9
	MOVD	72(RSP), R10
	MOVD	80(RSP), R11
	MOVD	88(RSP), R12
	MOVD	96(RSP), R13
	MOVD	104(RSP), R14
	MOVD	112(RSP), R15
	MOVD	120(RSP), R19
	MOVD	128(RSP), R20
	MOVD	136(RSP), R21
	MOVD	144(RSP), R22
	MOVD	152(RSP), R23
	MOVD	160(RSP), R24
	MOVD	168(RSP), R25
	MOVD	176(RSP), R26
	JMP	ret

// Note: these functions use a special calling convention to save generated code space.
// Arguments are passed in registers, but the space for those arguments are allocated
// in the caller's stack frame. These stubs write the args into that stack space and
// then tail call to the corresponding runtime handler.
// The tail call makes these stubs disappear in backtraces.
//
// Defined as ABIInternal since the compiler generates ABIInternal
// calls to it directly and it does not use the stack-based Go ABI.
TEXT runtime·panicIndex<ABIInternal>(SB),NOSPLIT,$0-16
	JMP	runtime·goPanicIndex<ABIInternal>(SB)
TEXT runtime·panicIndexU<ABIInternal>(SB),NOSPLIT,$0-16
	JMP	runtime·goPanicIndexU<ABIInternal>(SB)
TEXT runtime·panicSliceAlen<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R1, R0
	MOVD	R2, R1
	JMP	runtime·goPanicSliceAlen<ABIInternal>(SB)
TEXT runtime·panicSliceAlenU<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R1, R0
	MOVD	R2, R1
	JMP	runtime·goPanicSliceAlenU<ABIInternal>(SB)
TEXT runtime·panicSliceAcap<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R1, R0
	MOVD	R2, R1
	JMP	runtime·goPanicSliceAcap<ABIInternal>(SB)
TEXT runtime·panicSliceAcapU<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R1, R0
	MOVD	R2, R1
	JMP	runtime·goPanicSliceAcapU<ABIInternal>(SB)
TEXT runtime·panicSliceB<ABIInternal>(SB),NOSPLIT,$0-16
	JMP	runtime·goPanicSliceB<ABIInternal>(SB)
TEXT runtime·panicSliceBU<ABIInternal>(SB),NOSPLIT,$0-16
	JMP	runtime·goPanicSliceBU<ABIInternal>(SB)
TEXT runtime·panicSlice3Alen<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R2, R0
	MOVD	R3, R1
	JMP	runtime·goPanicSlice3Alen<ABIInternal>(SB)
TEXT runtime·panicSlice3AlenU<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R2, R0
	MOVD	R3, R1
	JMP	runtime·goPanicSlice3AlenU<ABIInternal>(SB)
TEXT runtime·panicSlice3Acap<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R2, R0
	MOVD	R3, R1
	JMP	runtime·goPanicSlice3Acap<ABIInternal>(SB)
TEXT runtime·panicSlice3AcapU<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R2, R0
	MOVD	R3, R1
	JMP	runtime·goPanicSlice3AcapU<ABIInternal>(SB)
TEXT runtime·panicSlice3B<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R1, R0
	MOVD	R2, R1
	JMP	runtime·goPanicSlice3B<ABIInternal>(SB)
TEXT runtime·panicSlice3BU<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R1, R0
	MOVD	R2, R1
	JMP	runtime·goPanicSlice3BU<ABIInternal>(SB)
TEXT runtime·panicSlice3C<ABIInternal>(SB),NOSPLIT,$0-16
	JMP	runtime·goPanicSlice3C<ABIInternal>(SB)
TEXT runtime·panicSlice3CU<ABIInternal>(SB),NOSPLIT,$0-16
	JMP	runtime·goPanicSlice3CU<ABIInternal>(SB)
TEXT runtime·panicSliceConvert<ABIInternal>(SB),NOSPLIT,$0-16
	MOVD	R2, R0
	MOVD	R3, R1
	JMP	runtime·goPanicSliceConvert<ABIInternal>(SB)
