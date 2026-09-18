/** Dashboard shell — sidebar + container. Route group; URL layout unchanged. */

import { ActiveNav } from "@/components/active-nav";
import { LogoutButton } from "@/components/logout-button";

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

export default function DashboardLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
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
  );
}
