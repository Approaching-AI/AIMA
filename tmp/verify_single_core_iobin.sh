#!/usr/bin/env bash
set -euo pipefail
cd ~/mlir-aie/programming_examples/basic/matrix_multiplication/single_core
python3 - <<'PY'
import numpy as np
M=64;K=64;N=64
rng=np.random.default_rng(123)
A=rng.integers(-8,8,size=(M,K),dtype=np.int16)
B=rng.integers(-8,8,size=(K,N),dtype=np.int16)
C=(A.astype(np.int32)@B.astype(np.int32)).astype(np.int32)
A.tofile('/tmp/mm_A_i16.bin')
B.tofile('/tmp/mm_B_i16.bin')
C.tofile('/tmp/mm_C_ref_i32.bin')
print('generated')
PY
XRT_HACK_UNSECURE_LOADING_XCLBIN=1 ./single_core.exe \
  -x build/final_64x64x64_32x32x32.xclbin \
  -i build/insts_64x64x64_32x32x32.txt \
  -k MLIR_AIE -M 64 -K 64 -N 64 \
  --verify=false --warmup 0 --iters 1 \
  --a_bin /tmp/mm_A_i16.bin --b_bin /tmp/mm_B_i16.bin --c_bin /tmp/mm_C_out_i32.bin \
  > /tmp/single_core_iobin.log 2>&1
python3 - <<'PY'
import numpy as np
ref=np.fromfile('/tmp/mm_C_ref_i32.bin',dtype=np.int32)
out=np.fromfile('/tmp/mm_C_out_i32.bin',dtype=np.int32)
print('ref_size',ref.size,'out_size',out.size)
if ref.size!=out.size:
    print('SIZE_MISMATCH')
    raise SystemExit(2)
neq=int((ref!=out).sum())
mx=int(np.max(np.abs(ref-out))) if ref.size else 0
print('neq',neq,'max_abs_diff',mx)
print('match',neq==0)
PY
tail -n 40 /tmp/single_core_iobin.log
