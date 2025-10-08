// jq.web.worker.ts
import "./assets/wasm/wasm_exec.js";
import type {
  ExecuteJQMessage,
  SymbolicExecuteJQMessage,
  FormatJQMessage,
} from "./bridge";

const _wasmExecutors = {
  ExecuteJQ: (..._: any): any => false,
  SymbolicExecuteJQ: (..._: any): any => false,
  FormatJQ: (..._: any): any => false,
} as const;

type MessageHandlers = {
  [K in keyof typeof _wasmExecutors]: (payload: any) => Promise<any>;
};

const messageHandlers: MessageHandlers = {
  ExecuteJQ: async (payload: ExecuteJQMessage["Request"]["payload"]) => {
    return exec("ExecuteJQ", payload.query, payload.jsonInput);
  },
  SymbolicExecuteJQ: async (
    payload: SymbolicExecuteJQMessage["Request"]["payload"],
  ) => {
    return exec("SymbolicExecuteJQ", payload.oasYAML);
  },
  FormatJQ: async (payload: FormatJQMessage["Request"]["payload"]) => {
    return exec("FormatJQ", payload.query);
  },
};

let instantiated = false;

async function fetchWasm(): Promise<ArrayBuffer> {
  // Dynamic import to avoid bundling WASM into the bundle
  const url = (await import('./assets/wasm/engine.latest.wasm.br?url')).default;
  const response = await fetch(url);

  if (!response.body) {
    throw new Error('Failed to fetch WASM: response body is null');
  }

  return response.arrayBuffer();
}

async function Instantiate() {
  if (instantiated) {
    return;
  }
  const go = new Go();
  const wasmBuffer = await fetchWasm();
  const result = await WebAssembly.instantiate(wasmBuffer, go.importObject);
  go.run(result.instance);
  for (const funcName of Object.keys(_wasmExecutors)) {
    // @ts-ignore
    if (!globalThis[funcName]) {
      throw new Error("missing expected function " + funcName);
    }
    // @ts-ignore
    _wasmExecutors[funcName] = globalThis[funcName];
  }
  instantiated = true;
}

async function exec(funcName: keyof typeof _wasmExecutors, ...args: any) {
  if (!instantiated) {
    await Instantiate();
  }
  if (!_wasmExecutors[funcName]) {
    throw new Error("not defined");
  }
  return _wasmExecutors[funcName](...args);
}

self.onmessage = async (
  event: MessageEvent<
    | ExecuteJQMessage["Request"]
    | SymbolicExecuteJQMessage["Request"]
    | FormatJQMessage["Request"]
  >,
) => {
  const { type, payload } = event.data;
  try {
    const handler = messageHandlers[type];
    if (handler) {
      const result = await handler(payload);
      self.postMessage({ type: `${type}Result`, payload: result });
    } else {
      throw new Error(`Unknown message type: ${type}`);
    }
  } catch (err: any) {
    if (err && err.message) {
      self.postMessage({ type: `${type}Error`, error: err.message });
    } else {
      self.postMessage({ type: `${type}Error`, error: "unknown error" });
    }
  }
};
