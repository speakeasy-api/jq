import { useState, useEffect, useCallback } from 'react';
import Editor from '@monaco-editor/react';
import { symbolicExecuteJQPipeline, PipelineResult } from '../bridge';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';

const DEFAULT_OAS = `openapi: 3.1.0
info:
  title: Symbolic Execution Demo
  version: 1.0.0
components:
  schemas:
    UserInput:
      type: object
      x-speakeasy-transform-from-json: >
        jq {userId: .id, displayName: .name,
            tier: (if .score >= 90 then "gold" else "silver" end),
            location: {city: .address.city, zip: .address.postalCode}}
      x-speakeasy-transform-to-json: >
        jq {id: .userId, name: .displayName,
            score: (if .tier == "gold" then 95 else 50 end),
            address: {city: .location.city, postalCode: .location.zip}}
      properties:
        id:
          type: integer
        name:
          type: string
        score:
          type: integer
        address:
          type: object
          properties:
            city:
              type: string
            postalCode:
              type: string
`;

export function SymbolicTab() {
  const [oasInput, setOasInput] = useState(DEFAULT_OAS);
  const [result, setResult] = useState<PipelineResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isExecuting, setIsExecuting] = useState(false);

  const transformOAS = useCallback(async () => {
    if (!oasInput.trim()) {
      setResult(null);
      setError(null);
      return;
    }

    setIsExecuting(true);
    setError(null);

    try {
      const pipelineResult = await symbolicExecuteJQPipeline(oasInput);
      setResult(pipelineResult);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setResult(null);
    } finally {
      setIsExecuting(false);
    }
  }, [oasInput]);

  // Auto-transform when input changes
  useEffect(() => {
    const timeoutId = setTimeout(() => {
      transformOAS();
    }, 500); // Debounce for 500ms

    return () => clearTimeout(timeoutId);
  }, [transformOAS]);

  return (
    <PanelGroup direction="horizontal" className="h-full">
      {/* Panel 1 - Original OAS */}
      <Panel defaultSize={33} minSize={20}>
        <div className="flex flex-col h-full border-r">
          <div className="px-4 py-2 border-b bg-muted/40">
            <h3 className="text-sm font-medium">Original OpenAPI Specification</h3>
          </div>
          <div className="flex-1 overflow-hidden">
            <Editor
              height="100%"
              defaultLanguage="yaml"
              value={oasInput}
              onChange={(value) => setOasInput(value || '')}
              theme="vs-dark"
              options={{
                minimap: { enabled: false },
                fontSize: 14,
                lineNumbers: 'on',
                scrollBeyondLastLine: false,
                automaticLayout: true,
              }}
            />
          </div>
        </div>
      </Panel>

      <PanelResizeHandle className="w-1 bg-border hover:bg-primary transition-colors" />

      {/* Panel 2 - After from-json */}
      <Panel defaultSize={33} minSize={20}>
        <div className="flex flex-col h-full border-r">
          <div className="px-4 py-2 border-b bg-muted/40 flex items-center justify-between">
            <h3 className="text-sm font-medium">Apply x-speakeasy-transform-from-json</h3>
            {isExecuting && (
              <span className="text-xs text-muted-foreground">Transforming...</span>
            )}
          </div>
          <div className="flex-1 overflow-hidden">
            {error ? (
              <div className="p-4 overflow-auto h-full">
                <div className="bg-red-500/10 border border-red-500/50 rounded-lg p-4">
                  <h4 className="text-red-500 font-semibold mb-2">Transformation Failed</h4>
                  <pre className="text-red-400 font-mono text-sm whitespace-pre-wrap">
                    {error}
                  </pre>
                </div>
              </div>
            ) : result ? (
              <Editor
                height="100%"
                defaultLanguage="yaml"
                value={result.panel2}
                theme="vs-dark"
                options={{
                  readOnly: true,
                  minimap: { enabled: false },
                  fontSize: 14,
                  lineNumbers: 'on',
                  scrollBeyondLastLine: false,
                  automaticLayout: true,
                }}
              />
            ) : (
              <div className="flex items-center justify-center h-full text-muted-foreground">
                <p>Panel 2 will show after from-json transformation</p>
              </div>
            )}
          </div>
        </div>
      </Panel>

      <PanelResizeHandle className="w-1 bg-border hover:bg-primary transition-colors" />

      {/* Panel 3 - After to-json */}
      <Panel defaultSize={34} minSize={20}>
        <div className="flex flex-col h-full">
          <div className="px-4 py-2 border-b bg-muted/40">
            <h3 className="text-sm font-medium">Apply x-speakeasy-transform-to-json</h3>
          </div>
          <div className="flex-1 overflow-hidden">
            {result ? (
              <Editor
                height="100%"
                defaultLanguage="yaml"
                value={result.panel3}
                theme="vs-dark"
                options={{
                  readOnly: true,
                  minimap: { enabled: false },
                  fontSize: 14,
                  lineNumbers: 'on',
                  scrollBeyondLastLine: false,
                  automaticLayout: true,
                }}
              />
            ) : (
              <div className="flex items-center justify-center h-full text-muted-foreground">
                <p>Panel 3 will show after to-json transformation</p>
              </div>
            )}
          </div>
        </div>
      </Panel>
    </PanelGroup>
  );
}
