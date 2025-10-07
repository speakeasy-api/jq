# JQ Playground

A web-based playground for testing JQ queries and symbolic execution with OpenAPI specifications.

## Features

- **Execute Tab**: Test JQ queries against JSON input in real-time
  - Query editor (top-left)
  - JSON input editor (bottom-left)
  - Output viewer (right)

- **Symbolic Tab**: Validate OpenAPI specifications
  - OAS YAML editor (left)
  - Validation results (right)

## Development

### Prerequisites

- Go 1.24+ (for building WASM)
- Node.js 18+ with pnpm

### Setup

1. Build the WASM module:
   ```bash
   cd ..
   make build-wasm
   ```

2. Install web dependencies:
   ```bash
   cd web
   pnpm install
   ```

3. Start the development server:
   ```bash
   pnpm dev
   ```

4. Open http://localhost:5173 in your browser

### Building for Production

```bash
# Build WASM
make build-wasm

# Build web app
cd web
pnpm build
```

## Architecture

- **cmd/wasm/**: Go WASM bindings for JQ execution and symbolic execution
- **web/src/bridge.ts**: TypeScript bridge for calling WASM functions
- **web/src/components/**: React components for Execute and Symbolic tabs
- **web/src/Playground.tsx**: Main playground component with tab navigation
