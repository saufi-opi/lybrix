/** Model registry (2.0.1 / Workstream 1 → WeKnora revamp): embedding models,
 * rerankers, and provider connections on one tabbed page. The embedding
 * registry backs every collection's mandatory embedding binding; the
 * reranker registry backs the optional cross-encoder stage; the connections
 * tab dry-run-probes every registered endpoint. */
import { ModelsManager } from "@/components/models-manager";
import { ProviderConnections } from "@/components/provider-connections";
import { RerankModelsManager } from "@/components/rerank-models-manager";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

export const dynamic = "force-dynamic";

export default function ModelsPage() {
  return (
    <Tabs defaultValue="embeddings">
      <TabsList>
        <TabsTrigger value="embeddings">Embedding models</TabsTrigger>
        <TabsTrigger value="rerankers">Rerankers</TabsTrigger>
        <TabsTrigger value="connections">Provider Connections</TabsTrigger>
      </TabsList>
      <TabsContent value="embeddings" className="mt-4">
        <ModelsManager />
      </TabsContent>
      <TabsContent value="rerankers" className="mt-4">
        <RerankModelsManager />
      </TabsContent>
      <TabsContent value="connections" className="mt-4">
        <ProviderConnections />
      </TabsContent>
    </Tabs>
  );
}
