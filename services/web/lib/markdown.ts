/** Markdown pre-normalization for chunk text.
 *
 * Chunk text is a verbatim slice of the parser's GFM markdown (BACKLOG R-26),
 * so it needs almost no repair before rendering — unlike WeKnora's
 * `processMarkdown`, which exists to undo MarkItDown's HTML entities and
 * `<p>`-wrapped table rows. The one real gap is LaTeX: the chunker treats
 * `$$…$$` as a protected region (internal/chunker/protected.go), but parsed
 * documents also arrive with `\[…\]` / `\(…\)` delimiters, which remark-math
 * does not recognize on its own.
 */

/** Rewrite `\[…\]` → `$$…$$` and `\(…\)` → `$…$` so remark-math renders them.
 * Ported from WeKnora's `preprocessMathDelimiters` (doc-content.vue:518-525). */
export function normalizeMathDelimiters(text: string): string {
  return text
    .replace(/\\\[([\s\S]*?)\\\]/g, (_m, body: string) => `$$${body}$$`)
    .replace(/\\\(([\s\S]*?)\\\)/g, (_m, body: string) => `$${body}$`);
}

/** Strip a leading YAML frontmatter block, if the parser emitted one. */
export function stripFrontmatter(text: string): string {
  return text.replace(/^\s*---\r?\n[\s\S]*?\r?\n---\r?\n/, "");
}

/** Full pre-render pass: frontmatter out, math delimiters normalized. */
export function prepareChunkMarkdown(text: string): string {
  return normalizeMathDelimiters(stripFrontmatter(text));
}
