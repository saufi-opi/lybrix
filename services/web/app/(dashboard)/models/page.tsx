/** Model registry (2.0.1 / Workstream 1): embedding models + rerankers on
 * one tabbed page. The embedding registry backs every collection's
 * mandatory embedding binding; the reranker registry backs the optional
 * cross-encoder stage. */

import { ModelsManager } from "@/components/models-manager";
import { RerankModelsManager } from "@/components/rerank-models-manager";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

export const dynamic = "force-dynamic";

export default function ModelsPage() {
  return (
    <Tabs defaultValue="embeddings">
      <TabsList>
        <TabsTrigger value="embeddings">Embedding models</TabsTrigger>
        <TabsTrigger value="rerankers">Rerankers</TabsTrigger>
      </TabsList>
      <TabsContent value="embeddings" className="mt-4">
        <ModelsManager />
      </TabsContent>
      <TabsContent value="rerankers" className="mt-4">
        <RerankModelsManager />
      </TabsContent>
    </Tabs>
  );
}
