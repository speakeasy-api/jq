# JQ Web Playground

A web playground for JQ with two modes: Execute and Symbolic.

## 🎯 Features

### Execute Tab
- **Query Editor** (top-left): Write JQ queries
- **JSON Input** (bottom-left): Provide JSON data to query
- **Output** (right): View results in real-time
- Auto-execution with 300ms debounce

### Symbolic Tab
- **OAS Input** (left): Paste OpenAPI Specification in YAML
- **Validation Result** (right): See success/error with parsed document info
- Auto-validation with 500ms debounce
- Uses `github.com/speakeasy-api/openapi` to parse and validate

## 📁 Project Structure

```
├── cmd/wasm/
│   └── functions.go           # WASM bindings (ExecuteJQ, SymbolicExecuteJQ)
├── web/
│   ├── src/
│   │   ├── bridge.ts          # TypeScript WASM bridge
│   │   ├── Playground.tsx     # Main component with tabs
│   │   ├── components/
│   │   │   ├── ExecuteTab.tsx # Execute mode UI
│   │   │   └── SymbolicTab.tsx # Symbolic mode UI
│   │   └── assets/wasm/       # WASM artifacts (gitignored)
│   ├── package.json
│   └── README.md
├── build.sh                   # WASM build script
├── Makefile                   # `make build-wasm` target
└── .gitignore                 # Includes Ref/ and web artifacts
```

## 🚀 Getting Started

### 1. Build WASM
```bash
make build-wasm
```

This creates:
- `web/src/assets/wasm/lib.wasm`
- `web/src/assets/wasm/wasm_exec.js`

### 2. Install Dependencies
```bash
cd web
pnpm install
```

### 3. Run Development Server
```bash
pnpm dev
```

Open http://localhost:5173

## 🔧 How It Works

### WASM Functions

**ExecuteJQ(query, jsonInput)**
- Parses JQ query using `gojq.Parse`
- Compiles with `gojq.Compile`
- Executes against JSON input
- Returns formatted JSON results or error

**SymbolicExecuteJQ(oasYAML)**
- Parses OpenAPI YAML using `gopkg.in/yaml.v3`
- Loads document with `openapi.LoadFromNode`
- Validates with `doc.Validate`
- Returns success with doc info or validation error

### Module Changes
- Updated `go.mod` from `github.com/itchyny/gojq` to `github.com/speakeasy-api/jq`
- Added `github.com/speakeasy-api/openapi` dependency (already present)

## 🎨 UI Design

Styled similarly to:
- https://github.com/speakeasy-api/jsonpath playground
- https://play.jqlang.org/

Uses:
- Monaco Editor for code editing
- React Resizable Panels for split layouts
- Tailwind CSS for styling
- Dark theme by default

## 📝 Future Iterations

The symbolic tab currently validates OAS. Future enhancements could:
- Run symbolic execution over schemas
- Show schema inference results
- Visualize execution paths
- Test queries against schema constraints
