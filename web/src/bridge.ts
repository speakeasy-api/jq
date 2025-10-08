// Bridge for loading and interacting with WASM module via web worker
import JQWorker from "./jq.web.worker.ts?worker";

const wasmWorker = new JQWorker();
let messageQueue: {
  resolve: Function;
  reject: Function;
  message: any;
  supercede: boolean;
}[] = [];
let isProcessing = false;

function processQueue() {
  if (isProcessing || messageQueue.length === 0) return;

  isProcessing = true;
  const { resolve, reject, message } = messageQueue.shift()!;

  wasmWorker.postMessage(message);
  wasmWorker.onmessage = (event: MessageEvent<any>) => {
    if (event.data.type.endsWith("Result")) {
      // Reject all superceded messages before resolving the current message
      const supercedeMessages = messageQueue.filter((_, index) => {
        return messageQueue.slice(index + 1).some((later) => later.supercede);
      });
      supercedeMessages.forEach((m) => m.reject(new Error("supercedeerror")));
      messageQueue = messageQueue.filter((m) => !supercedeMessages.includes(m));
      resolve(event.data.payload);
    } else if (event.data.type.endsWith("Error")) {
      reject(new Error(event.data.error));
    }
    isProcessing = false;
    processQueue();
  };
}

function sendMessage(message: any, supercede = false): Promise<any> {
  return new Promise((resolve, reject) => {
    messageQueue.push({ resolve, reject, message, supercede });
    processQueue();
  });
}

export type ExecuteJQMessage = {
  Request: {
    type: "ExecuteJQ";
    payload: {
      query: string;
      jsonInput: string;
    };
  };
  Response:
    | {
        type: "ExecuteJQResult";
        payload: string;
      }
    | {
        type: "ExecuteJQError";
        error: string;
      };
};

export type SymbolicExecuteJQMessage = {
  Request: {
    type: "SymbolicExecuteJQ";
    payload: {
      oasYAML: string;
    };
  };
  Response:
    | {
        type: "SymbolicExecuteJQResult";
        payload: string;
      }
    | {
        type: "SymbolicExecuteJQError";
        error: string;
      };
};

export type FormatJQMessage = {
  Request: {
    type: "FormatJQ";
    payload: {
      query: string;
    };
  };
  Response:
    | {
        type: "FormatJQResult";
        payload: string;
      }
    | {
        type: "FormatJQError";
        error: string;
      };
};

export type PipelineResult = {
  panel1: string;
  panel2: string;
  panel3: string;
  appliedFromJson: boolean;
  appliedToJson: boolean;
  warnings: string[];
};

export type SymbolicExecuteJQPipelineMessage = {
  Request: {
    type: "SymbolicExecuteJQPipeline";
    payload: {
      oasYAML: string;
    };
  };
  Response:
    | {
        type: "SymbolicExecuteJQPipelineResult";
        payload: string; // JSON-serialized PipelineResult
      }
    | {
        type: "SymbolicExecuteJQPipelineError";
        error: string;
      };
};

export function executeJQ(
  query: string,
  jsonInput: string,
  supercede = false,
): Promise<string> {
  return sendMessage(
    {
      type: "ExecuteJQ",
      payload: { query, jsonInput },
    } satisfies ExecuteJQMessage["Request"],
    supercede,
  );
}

export function symbolicExecuteJQ(
  oasYAML: string,
  supercede = false,
): Promise<string> {
  return sendMessage(
    {
      type: "SymbolicExecuteJQ",
      payload: { oasYAML },
    } satisfies SymbolicExecuteJQMessage["Request"],
    supercede,
  );
}

export function symbolicExecuteJQPipeline(
  oasYAML: string,
  supercede = false,
): Promise<PipelineResult> {
  return sendMessage(
    {
      type: "SymbolicExecuteJQPipeline",
      payload: { oasYAML },
    } satisfies SymbolicExecuteJQPipelineMessage["Request"],
    supercede,
  ).then((result: string) => JSON.parse(result));
}

export function formatJQ(query: string, supercede = false): Promise<string> {
  return sendMessage(
    {
      type: "FormatJQ",
      payload: { query },
    } satisfies FormatJQMessage["Request"],
    supercede,
  );
}
