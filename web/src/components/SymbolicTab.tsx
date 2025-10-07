import { useState, useEffect, useCallback } from 'react';
import Editor from '@monaco-editor/react';
import { symbolicExecuteJQ } from '../bridge';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';

const DEFAULT_OAS = `openapi: 3.0.0
info:
  title: Sample API
  version: 1.0.0
  description: A simple API for testing
paths:
  /users:
    get:
      summary: Get all users
      responses:
        '200':
          description: Successful response
          content:
            application/json:
              schema:
                type: array
                items:
                  type: object
                  properties:
                    id:
                      type: integer
                    name:
                      type: string
                    email:
                      type: string
  /users/{id}:
    get:
      summary: Get user by ID
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: integer
      responses:
        '200':
          description: Successful response
          content:
            application/json:
              schema:
                type: object
                properties:
                  id:
                    type: integer
                  name:
                    type: string
                  email:
                    type: string
`;

export function SymbolicTab() {
  const [oasInput, setOasInput] = useState(DEFAULT_OAS);
  const [output, setOutput] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isExecuting, setIsExecuting] = useState(false);

  const validateOAS = useCallback(async () => {
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

  // Auto-validate when input changes
  useEffect(() => {
    const timeoutId = setTimeout(() => {
      validateOAS();
    }, 500); // Debounce for 500ms

    return () => clearTimeout(timeoutId);
  }, [validateOAS]);

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

      {/* Right side - Validation Result */}
      <Panel defaultSize={50} minSize={30}>
        <div className="flex flex-col h-full">
          <div className="px-4 py-2 border-b bg-muted/40 flex items-center justify-between">
            <h3 className="text-sm font-medium">Validation Result</h3>
            {isExecuting && (
              <span className="text-xs text-muted-foreground">Validating...</span>
            )}
          </div>
          <div className="flex-1">
            {error ? (
              <div className="p-4">
                <div className="bg-red-500/10 border border-red-500/50 rounded-lg p-4">
                  <h4 className="text-red-500 font-semibold mb-2">Validation Failed</h4>
                  <pre className="text-red-400 font-mono text-sm whitespace-pre-wrap">
                    {error}
                  </pre>
                </div>
              </div>
            ) : output ? (
              <div className="p-4">
                <div className="bg-green-500/10 border border-green-500/50 rounded-lg p-4">
                  <h4 className="text-green-500 font-semibold mb-2">✓ Validation Successful</h4>
                  <Editor
                    height="300px"
                    defaultLanguage="json"
                    value={output}
                    theme="vs-dark"
                    options={{
                      readOnly: true,
                      minimap: { enabled: false },
                      fontSize: 14,
                      lineNumbers: 'off',
                      scrollBeyondLastLine: false,
                      automaticLayout: true,
                      folding: false,
                      lineDecorationsWidth: 0,
                      lineNumbersMinChars: 0,
                    }}
                  />
                </div>
              </div>
            ) : (
              <div className="flex items-center justify-center h-full text-muted-foreground">
                <p>Enter an OpenAPI specification to validate</p>
              </div>
            )}
          </div>
        </div>
      </Panel>
    </PanelGroup>
  );
}
