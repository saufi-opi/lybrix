/* third_party/anydoc-go/include/anydoc.h — extern "C" surface of the
 * Firecrawl anydoc Rust crate compiled as a static C library.
 *
 * Built by scripts/build-anydoc-lib.sh:
 *   cargo build --release        # staticlib crate-type
 *   cbindgen                     # this header (vendored copy checked in)
 *   cp target/release/libanydoc_go.a lib/$(target_triple)/
 *
 * Thread safety: anydoc keeps per-thread error registers; every call must
 * run on a pinned OS thread (Go: runtime.LockOSThread around the call).
 */
#ifndef ANYDOC_H
#define ANYDOC_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Convert a document (pdf/docx/pptx/xlsx/txt) to GitHub-flavored markdown.
 *
 * buf/len        input bytes
 * format         0 = pdf, 1 = docx, 2 = pptx, 3 = xlsx, 4 = txt
 * out_buf        receives a malloc'd buffer with the markdown bytes
 * out_len        receives the markdown length in bytes
 * err_buf        caller-allocated (>=256 bytes); receives a NUL-terminated
 *                error message on failure, untouched on success
 *
 * Returns 0 on success, non-zero on failure (err_buf then carries detail).
 * On success the caller owns *out_buf and must release it with
 * anydoc_free_buffer. On failure *out_buf is untouched.
 */
int anydoc_convert(const unsigned char *buf, size_t len, int format,
                   unsigned char **out_buf, size_t *out_len,
                   char *err_buf);

/* Release a buffer produced by anydoc_convert. NULL is a no-op. */
void anydoc_free_buffer(unsigned char *buf);

#ifdef __cplusplus
}
#endif

#endif /* ANYDOC_H */
