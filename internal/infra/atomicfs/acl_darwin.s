// The libSystem trampoline aclOf calls getattrlist through, as x/sys's
// zsyscall_darwin_*.s are written for both architectures.

#include "textflag.h"

TEXT libc_getattrlist_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_getattrlist(SB)
GLOBL	·libcGetattrlistTrampolineAddr(SB), RODATA, $8
DATA	·libcGetattrlistTrampolineAddr(SB)/8, $libc_getattrlist_trampoline<>(SB)
