/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.2.0
 */

// mingw libstdc++ (GCC >= 13) no longer defines the TLS globals behind
// std::call_once's legacy inline path, but the prebuilt duckdb-go-bindings
// windows static libs (built with an older toolchain) still reference them
// through emulated TLS: __emutls_v._ZSt11__once_call and
// __emutls_v._ZSt15__once_callable. Defining them here satisfies the linker;
// std::__once_proxy in current libstdc++ consumes them exactly as the old
// ABI expects.
namespace std {
__thread void* __once_callable;
__thread void (*__once_call)();
}
