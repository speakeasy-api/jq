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
    EntityResponse:
      type: object
      description: Extract nested ID to top-level with minimal references.
      x-speakeasy-transform-from-json: 'jq . + {id: .data.result[0].id}'
      x-speakeasy-transform-to-json: 'jq {data}'
      properties:
        data:
          type: object
          properties:
            result:
              type: array
              items:
                type: object
                properties:
                  id:
                    type: string
                  name:
                    type: string
                  active:
                    type: boolean
            meta:
              type: object
              properties:
                timestamp:
                  type: string
                  format: date-time
                version:
                  type: integer
    PaginatedItemsResponse:
      type: object
      description: Flattened pagination with normalized item status.
      x-speakeasy-transform-from-json: >
        jq {
          items: (.data.items // []) | map({
            id: .id,
            title: .title,
            status: (if (.active // false) then "active" else "inactive" end)
          }),
          hasMore: (.data.pagination.nextCursor != null),
          total: (.data.pagination.total // 0),
          nextCursor: (.data.pagination.nextCursor // null)
        }
      x-speakeasy-transform-to-json: >
        jq {
          data: {
            items: (.items // []) | map({
              id: .id,
              title: .title,
              active: (.status == "active")
            }),
            pagination: {
              nextCursor: .nextCursor,
              total: (.total // 0)
            }
          }
        }
      properties:
        data:
          type: object
          properties:
            items:
              type: array
              items:
                type: object
                properties:
                  id:
                    type: string
                  title:
                    type: string
                  active:
                    type: boolean
            pagination:
              type: object
              properties:
                nextCursor:
                  type: string
                  nullable: true
                total:
                  type: integer
    UserPreferences:
      type: object
      description: Flattened user profile with computed fullName.
      x-speakeasy-transform-from-json: >
        jq {
          userId: .data.user.id,
          email: .data.user.profile.contact.email,
          fullName: (.data.user.profile.name.first + " " + .data.user.profile.name.last),
          preferences: {
            theme: .data.user.profile.preferences.settings.theme,
            locale: .data.user.profile.preferences.locale,
            notifyEmail: .data.user.profile.preferences.settings.notifications.email,
            notifySms: .data.user.profile.preferences.settings.notifications.sms
          }
        }
      x-speakeasy-transform-to-json: >
        jq {
          data: {
            user: {
              id: .userId,
              profile: {
                name: {
                  first: (.fullName | split(" ") | .[0]),
                  last:  (.fullName | split(" ") | .[1:] | join(" "))
                },
                contact: { email: .email },
                preferences: {
                  locale: .preferences.locale,
                  settings: {
                    theme: .preferences.theme,
                    notifications: {
                      email: .preferences.notifyEmail,
                      sms: .preferences.notifySms
                    }
                  }
                }
              }
            }
          }
        }
      properties:
        data:
          type: object
          properties:
            user:
              type: object
              properties:
                id:
                  type: string
                profile:
                  type: object
                  properties:
                    name:
                      type: object
                      properties:
                        first:
                          type: string
                        last:
                          type: string
                    contact:
                      type: object
                      properties:
                        email:
                          type: string
                          format: email
                    preferences:
                      type: object
                      properties:
                        locale:
                          type: string
                        settings:
                          type: object
                          properties:
                            theme:
                              type: string
                            notifications:
                              type: object
                              properties:
                                email:
                                  type: boolean
                                sms:
                                  type: boolean
    TagList:
      type: object
      description: Enriched tag list with computed slug and length.
      x-speakeasy-transform-from-json: >
        jq {
          tags: (.tags // []) | map({
            value: .,
            slug: (. | ascii_downcase | gsub("[^a-z0-9]+"; "-") | gsub("(^-|-$)"; "")),
            length: (. | length)
          }),
          primarySlug: (
            (.tags[0] // null)
            | if . == null then null
              else (. | ascii_downcase | gsub("[^a-z0-9]+"; "-") | gsub("(^-|-$)"; ""))
              end
          )
        }
      x-speakeasy-transform-to-json: >
        jq {
          tags: (.tags // []) | map(.value)
        }
      properties:
        tags:
          type: array
          items:
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
