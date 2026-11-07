// Verify that a Kubernetes GPU allocation supports an actual CUDA kernel.
// Build: cc -O2 -Wall -Wextra -o cuda-smoke cuda_smoke.c -ldl
// Run inside a GPU-allocated workspace with NVIDIA's CDI/driver injection.
#define _POSIX_C_SOURCE 200809L
#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

typedef int CUresult;
typedef int CUdevice;
typedef void *CUcontext;
typedef void *CUmodule;
typedef void *CUfunction;
typedef uint64_t CUdeviceptr;

#define CUDA_SUCCESS 0
#define CUDA_CHECK(expr) do { \
    CUresult status_ = (expr); \
    if (status_ != CUDA_SUCCESS) { \
        fprintf(stderr, "CUDA error %d: %s\n", status_, #expr); \
        return 1; \
    } \
} while (0)

static const char kernel_ptx[] =
    ".version 8.0\n"
    ".target sm_80\n"
    ".address_size 64\n"
    ".visible .entry add_one(.param .u64 ptr) {\n"
    " .reg .b64 %rd<3>;\n"
    " .reg .b32 %r<3>;\n"
    " ld.param.u64 %rd1, [ptr];\n"
    " cvta.to.global.u64 %rd2, %rd1;\n"
    " ld.global.u32 %r1, [%rd2];\n"
    " add.u32 %r2, %r1, 1;\n"
    " st.global.u32 [%rd2], %r2;\n"
    " ret;\n"
    "}\n";

#define LOAD(name) do { \
    name = dlsym(handle, #name); \
    if (!name) { fprintf(stderr, "CUDA symbol unavailable: %s\n", #name); return 1; } \
} while (0)

int main(void) {
    void *handle = dlopen("libcuda.so.1", RTLD_NOW | RTLD_LOCAL);
    if (!handle) {
        fprintf(stderr, "CUDA driver unavailable: %s\n", dlerror());
        return 1;
    }
    CUresult (*cuInit)(unsigned int);
    CUresult (*cuDeviceGetCount)(int *);
    CUresult (*cuDeviceGet)(CUdevice *, int);
    CUresult (*cuCtxCreate_v2)(CUcontext *, unsigned int, CUdevice);
    CUresult (*cuCtxDestroy_v2)(CUcontext);
    CUresult (*cuMemAlloc_v2)(CUdeviceptr *, size_t);
    CUresult (*cuMemFree_v2)(CUdeviceptr);
    CUresult (*cuMemcpyHtoD_v2)(CUdeviceptr, const void *, size_t);
    CUresult (*cuMemcpyDtoH_v2)(void *, CUdeviceptr, size_t);
    CUresult (*cuModuleLoadData)(CUmodule *, const void *);
    CUresult (*cuModuleUnload)(CUmodule);
    CUresult (*cuModuleGetFunction)(CUfunction *, CUmodule, const char *);
    CUresult (*cuLaunchKernel)(CUfunction, unsigned int, unsigned int, unsigned int,
                             unsigned int, unsigned int, unsigned int,
                             unsigned int, void *, void **, void **);
    CUresult (*cuCtxSynchronize)(void);
    LOAD(cuInit);
    LOAD(cuDeviceGetCount);
    LOAD(cuDeviceGet);
    LOAD(cuCtxCreate_v2);
    LOAD(cuCtxDestroy_v2);
    LOAD(cuMemAlloc_v2);
    LOAD(cuMemFree_v2);
    LOAD(cuMemcpyHtoD_v2);
    LOAD(cuMemcpyDtoH_v2);
    LOAD(cuModuleLoadData);
    LOAD(cuModuleUnload);
    LOAD(cuModuleGetFunction);
    LOAD(cuLaunchKernel);
    LOAD(cuCtxSynchronize);

    CUDA_CHECK(cuInit(0));
    int count = 0;
    CUDA_CHECK(cuDeviceGetCount(&count));
    if (count < 1) {
        fputs("No CUDA devices visible to workload\n", stderr);
        return 1;
    }
    CUdevice device = 0;
    CUcontext context = NULL;
    CUmodule module = NULL;
    CUfunction function = NULL;
    CUdeviceptr allocation = 0;
    CUDA_CHECK(cuDeviceGet(&device, 0));
    CUDA_CHECK(cuCtxCreate_v2(&context, 0, device));
    CUDA_CHECK(cuMemAlloc_v2(&allocation, sizeof(uint32_t)));
    uint32_t input = 42, output = 0;
    CUDA_CHECK(cuMemcpyHtoD_v2(allocation, &input, sizeof(input)));
    CUDA_CHECK(cuModuleLoadData(&module, kernel_ptx));
    CUDA_CHECK(cuModuleGetFunction(&function, module, "add_one"));
    void *args[] = {&allocation};
    CUDA_CHECK(cuLaunchKernel(function, 1, 1, 1, 1, 1, 1, 0, NULL, args, NULL));
    CUDA_CHECK(cuCtxSynchronize());
    CUDA_CHECK(cuMemcpyDtoH_v2(&output, allocation, sizeof(output)));
    if (output != 43) {
        fprintf(stderr, "CUDA kernel result mismatch: %u\n", output);
        return 1;
    }
    CUDA_CHECK(cuModuleUnload(module));
    CUDA_CHECK(cuMemFree_v2(allocation));
    CUDA_CHECK(cuCtxDestroy_v2(context));
    printf("CUDA_KERNEL_OK devices=%d input=%u output=%u\n", count, input, output);
    dlclose(handle);
    return 0;
}
