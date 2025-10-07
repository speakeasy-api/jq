import { useState, useEffect, useCallback } from 'react';
import Editor from '@monaco-editor/react';
import { executeJQ, formatJQ } from '../bridge';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';

const DEFAULT_QUERY = '.';
const DEFAULT_JSON = `{
  "name": "John Doe",
  "age": 30,
  "email": "john@example.com",
  "address": {
    "street": "123 Main St",
    "city": "Springfield",
    "state": "IL"
  },
  "hobbies": ["reading", "hiking", "coding"]
}`;

export function ExecuteTab() {
  const [query, setQuery] = useState(DEFAULT_QUERY);
  const [jsonInput, setJsonInput] = useState(DEFAULT_JSON);
  const [output, setOutput] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isExecuting, setIsExecuting] = useState(false);
  const [isFormatting, setIsFormatting] = useState(false);

  const executeQuery = useCallback(async () => {
    if (!query.trim() || !jsonInput.trim()) {
      setOutput('');
      setError(null);
      return;
    }

    setIsExecuting(true);
    setError(null);

    try {
      const result = await executeJQ(query, jsonInput);
      setOutput(result);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setOutput('');
    } finally {
      setIsExecuting(false);
    }
  }, [query, jsonInput]);

  // Auto-execute when query or input changes
  useEffect(() => {
    const timeoutId = setTimeout(() => {
      executeQuery();
    }, 300); // Debounce for 300ms

    return () => clearTimeout(timeoutId);
  }, [executeQuery]);

  const handleFormat = async () => {
    if (!query.trim()) return;

    setIsFormatting(true);
    try {
      const formatted = await formatJQ(query);
      setQuery(formatted);
    } catch (err) {
      console.error('Failed to format query:', err);
    } finally {
      setIsFormatting(false);
    }
  };

  return (
    <PanelGroup direction="horizontal" className="h-full">
      {/* Left side - Query and JSON Input */}
      <Panel defaultSize={50} minSize={30}>
        <PanelGroup direction="vertical" className="h-full">
          {/* Query Editor */}
          <Panel defaultSize={30} minSize={20}>
            <div className="flex flex-col h-full border-r">
              <div className="px-4 py-2 border-b bg-muted/40 flex items-center justify-between">
                <h3 className="text-sm font-medium">JQ Query</h3>
                <button
                  onClick={handleFormat}
                  disabled={isFormatting}
                  className="text-xs px-2 py-1 rounded bg-primary/10 hover:bg-primary/20 text-foreground disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                >
                  {isFormatting ? 'Formatting...' : 'Format'}
                </button>
              </div>
              <div className="flex-1">
                <Editor
                  height="100%"
                  defaultLanguage="plaintext"
                  value={query}
                  onChange={(value) => setQuery(value || '')}
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

          <PanelResizeHandle className="h-1 bg-border hover:bg-primary transition-colors" />

          {/* JSON Input Editor */}
          <Panel defaultSize={70} minSize={20}>
            <div className="flex flex-col h-full border-r">
              <div className="px-4 py-2 border-b bg-muted/40">
                <h3 className="text-sm font-medium">JSON Input</h3>
              </div>
              <div className="flex-1">
                <Editor
                  height="100%"
                  defaultLanguage="json"
                  value={jsonInput}
                  onChange={(value) => setJsonInput(value || '')}
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
        </PanelGroup>
      </Panel>

      <PanelResizeHandle className="w-1 bg-border hover:bg-primary transition-colors" />

      {/* Right side - Output */}
      <Panel defaultSize={50} minSize={30}>
        <div className="flex flex-col h-full">
          <div className="px-4 py-2 border-b bg-muted/40 flex items-center justify-between">
            <h3 className="text-sm font-medium">Output</h3>
            {isExecuting && (
              <span className="text-xs text-muted-foreground">Executing...</span>
            )}
          </div>
          <div className="flex-1">
            {error ? (
              <div className="p-4 text-red-500 font-mono text-sm whitespace-pre-wrap">
                {error}
              </div>
            ) : (
              <Editor
                height="100%"
                defaultLanguage="json"
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
            )}
          </div>
        </div>
      </Panel>
    </PanelGroup>
  );
}
