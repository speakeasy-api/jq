import { useState, useEffect, useCallback } from 'react';
import Editor from '@monaco-editor/react';
import { symbolicExecuteJQ } from '../bridge';
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
    ProductInput:
      type: object
      x-speakeasy-transform-from-json: >
        jq {productId: .id, displayName: .name,
            total: (.price * .quantity),
            tags: (.tags | map({value: .}))}
      properties:
        id:
          type: string
        name:
          type: string
        price:
          type: number
        quantity:
          type: integer
        tags:
          type: array
          items:
            type: string
    CartInput:
      type: object
      x-speakeasy-transform-from-json: >
        jq {grandTotal: (.items | map(.price * .quantity) | add // 0),
            items: (.items | map({sku, total: (.price * .quantity)}))}
      properties:
        items:
          type: array
          items:
            type: object
            properties:
              sku:
                type: string
              price:
                type: number
              quantity:
                type: integer
`;

export function SymbolicTab() {
  const [oasInput, setOasInput] = useState(DEFAULT_OAS);
  const [output, setOutput] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isExecuting, setIsExecuting] = useState(false);

  const transformOAS = useCallback(async () => {
    if (!oasInput.trim()) {
      setOutput('');
      setError(null);
      return;
    }

    setIsExecuting(true);
    setError(null);

    try {
      const result = await symbolicExecuteJQ(oasInput);
      setOutput(result);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setOutput('');
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
      {/* Left side - OAS Input */}
      <Panel defaultSize={50} minSize={30}>
        <div className="flex flex-col h-full border-r">
          <div className="px-4 py-2 border-b bg-muted/40">
            <h3 className="text-sm font-medium">OpenAPI Specification (YAML)</h3>
          </div>
          <div className="flex-1">
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

      {/* Right side - Transformed OAS */}
      <Panel defaultSize={50} minSize={30}>
        <div className="flex flex-col h-full">
          <div className="px-4 py-2 border-b bg-muted/40 flex items-center justify-between">
            <h3 className="text-sm font-medium">New OpenAPI Specification</h3>
            {isExecuting && (
              <span className="text-xs text-muted-foreground">Transforming...</span>
            )}
          </div>
          <div className="flex-1">
            {error ? (
              <div className="p-4">
                <div className="bg-red-500/10 border border-red-500/50 rounded-lg p-4">
                  <h4 className="text-red-500 font-semibold mb-2">Transformation Failed</h4>
                  <pre className="text-red-400 font-mono text-sm whitespace-pre-wrap">
                    {error}
                  </pre>
                </div>
              </div>
            ) : output ? (
              <Editor
                height="100%"
                defaultLanguage="yaml"
                value={output}
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
                <p>Enter an OpenAPI specification with x-speakeasy-transform-from-json extensions to transform</p>
              </div>
            )}
          </div>
        </div>
      </Panel>
    </PanelGroup>
  );
}
