"use client";

/** GSAP ScrollTrigger stagger reveal — Phase 6 animation 1. Cards start at
 * y:40/opacity:0 and reveal 0.8s each, staggered 0.1s, when they scroll into
 * view. Registered only behind prefers-reduced-motion: no-preference AND a
 * fine pointer (mobile keeps the CSS fallback / opacity only). The CSS
 * .kb-card-reveal fallback handles no-JS and reduced-motion rendering. */

import { type ReactNode, useEffect } from "react";

export function GsapStagger({ children }: { children: ReactNode }) {
  useEffect(() => {
    const noPref = window.matchMedia("(prefers-reduced-motion: no-preference)").matches;
    const finePointer = window.matchMedia("(pointer: fine)").matches;
    if (!noPref || !finePointer) return;

    let cleanup = () => {};
    void (async () => {
      const [{ gsap }, { ScrollTrigger }] = await Promise.all([
        import("gsap"),
        import("gsap/ScrollTrigger"),
      ]);
      gsap.registerPlugin(ScrollTrigger);
      const cards = document.querySelectorAll<HTMLElement>(".kb-card-reveal");
      const ctx = gsap.context(() => {
        gsap.set(cards, { opacity: 0, y: 40 });
        ScrollTrigger.batch(cards, {
          start: "top 90%",
          once: true,
          onEnter: (batch) =>
            gsap.to(batch, {
              opacity: 1,
              y: 0,
              duration: 0.8,
              stagger: 0.1,
              ease: "power2.out",
              overwrite: true,
            }),
        });
      });
      cleanup = () => ctx.revert();
    })();

    return () => cleanup();
  }, []);

  return <>{children}</>;
}
