/** API keys (Phase 2 centerpiece): create / list / revoke via /api/admin. */

import { KeysManager } from "@/components/keys-manager";

export const dynamic = "force-dynamic";

export default function KeysPage() {
  return <KeysManager />;
}
