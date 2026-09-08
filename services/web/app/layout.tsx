import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "rag-platform admin",
  description: "Document Ingestion & Retrieval Platform — admin UI (PRD §8)",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <nav className="topnav">
          <a href="/">Dashboard</a>
          <a href="/documents">Documents</a>
          <a href="/upload">Upload</a>
          <a href="/logs">Logs</a>
          <a href="/collections">Collections</a>
          <a href="/settings">Settings</a>
        </nav>
        <main className="container">{children}</main>
      </body>
    </html>
  );
}
