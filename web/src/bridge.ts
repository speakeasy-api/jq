// Bridge for loading and interacting with WASM module
import wasmExec from './assets/wasm/wasm_exec.js?url';

let wasmInstance: WebAssembly.Instance | null = null;
let wasmReady = false;

// Declare the global functions that will be set by WASM
declare global {
  interface Window {
    ExecuteJQ: (query: string, jsonInput: string) => Promise<string>;
    SymbolicExecuteJQ: (oasYAML: string) => Promise<string>;
    FormatJQ: (query: string) => Promise<string>;
  }
}

async function loadWasm(): Promise<void> {
  if (wasmReady) {
    return;
  }

  // Load wasm_exec.js
  const script = document.createElement('script');
  script.src = wasmExec;
  await new Promise((resolve, reject) => {
    script.onload = resolve;
    script.onerror = reject;
    document.head.appendChild(script);
  });

  // @ts-expect-error Go is defined by wasm_exec.js
  const go = new window.Go();

  // Fetch and instantiate the WASM module
  const response = await fetch('/src/assets/wasm/lib.wasm');
  const buffer = await response.arrayBuffer();
  const result = await WebAssembly.instantiate(buffer, go.importObject);

  wasmInstance = result.instance;

  // Run the Go program
  go.run(wasmInstance);

  wasmReady = true;
}

// Ensure WASM is loaded before calling functions
async function ensureWasmLoaded(): Promise<void> {
  if (!wasmReady) {
    await loadWasm();
  }
}

// Export wrapped functions
export async function executeJQ(query: string, jsonInput: string): Promise<string> {
  await ensureWasmLoaded();
  return window.ExecuteJQ(query, jsonInput);
}

export async function symbolicExecuteJQ(oasYAML: string): Promise<string> {
  await ensureWasmLoaded();
  return window.SymbolicExecuteJQ(oasYAML);
}

export async function formatJQ(query: string): Promise<string> {
  await ensureWasmLoaded();
  return window.FormatJQ(query);
}

// Initialize WASM on module load
loadWasm().catch(console.error);
