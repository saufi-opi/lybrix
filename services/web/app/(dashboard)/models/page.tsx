/** Model registry (2.0.1): create / test / edit / delete embedding models.
 * The registry backs every collection's mandatory embedding binding. */

import { ModelsManager } from "@/components/models-manager";

export const dynamic = "force-dynamic";

export default function ModelsPage() {
  return <ModelsManager />;
}
