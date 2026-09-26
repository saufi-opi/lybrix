"use client";

/** Shared markdown renderer for chunk text.
 *
 * Chunk text is a verbatim slice of the parser's GFM markdown (BACKLOG R-26),
 * so it renders as formatted prose, tables, lists and highlighted code instead
 * of a literal `|`/`#`/``` stream. Ported in spirit from WeKnora's chunk
 * inspector (`frontend/src/components/doc-content.vue`), which runs marked +
 * GFM + KaTeX + highlight.js — but rendered through react-markdown so there is
 * no raw-HTML injection and therefore no DOMPurify pass to maintain.
 *
 * `skipHtml` drops any embedded HTML rather than rendering it, which is what
 * lets us render untrusted corpus text without a sanitizer.
 */

import { memo } from "react";
import ReactMarkdown from "react-markdown";
import rehypeHighlight from "rehype-highlight";
import rehypeKatex from "rehype-katex";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import { cn } from "@/lib/utils";

import "katex/dist/katex.min.css";

/* Module-level so the plugin arrays are not re-allocated on every render. */
const REMARK_PLUGINS = [remarkGfm, remarkMath];
const REHYPE_PLUGINS = [rehypeHighlight, rehypeKatex];

/** Renders markdown into the themed `.md-content` block (styles in
 * app/globals.css). Memoized: a 50-row chunk page must not re-parse every
 * chunk when one row's expansion state changes. */
export const Markdown = memo(function Markdown({
  children,
  className,
}: {
  children: string;
  className?: string;
}) {
  return (
    <div className={cn("md-content", className)}>
      <ReactMarkdown remarkPlugins={REMARK_PLUGINS} rehypePlugins={REHYPE_PLUGINS} skipHtml>
        {children}
      </ReactMarkdown>
    </div>
  );
});
