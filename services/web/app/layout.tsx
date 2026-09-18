import type { Metadata } from "next";
import { Inter } from "next/font/google";
import "./globals.css";
import { ActiveNav } from "@/components/active-nav";
import { LogoutButton } from "@/components/logout-button";

const inter = Inter({
  subsets: ["latin"],
  variable: "--font-inter",
  display: "swap",
});

export const metadata: Metadata = {
  title: "rag-platform admin",
  description: "Document Ingestion & Retrieval Platform — admin UI (PRD §8)",
};

const NAV = [
  { href: "/", label: "Dashboard" },
  { href: "/pipeline", label: "Pipeline" },
  { href: "/documents", label: "Documents" },
  { href: "/upload", label: "Upload" },
  { href: "/logs", label: "Logs" },
  { href: "/collections", label: "Collections" },
  { href: "/keys", label: "API Keys" },
  { href: "/usage", label: "Usage" },
  { href: "/settings", label: "Settings" },
];

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" className={inter.variable}>
      <body>
        <div className="shell">
          <nav className="sidebar">
            <span className="brand">
              rag-platform
              <small>ingestion &amp; retrieval</small>
            </span>
            <ActiveNav items={NAV} />
            <div className="sidebar-footer">
              <LogoutButton />
            </div>
          </nav>
          <main className="container">{children}</main>
        </div>
      </body>
    </html>
  );
}
