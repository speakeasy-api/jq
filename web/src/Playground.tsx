import { useState } from 'react';
import { ExecuteTab } from './components/ExecuteTab';
import { SymbolicTab } from './components/SymbolicTab';

type Tab = 'execute' | 'symbolic';

export function Playground() {
  const [activeTab, setActiveTab] = useState<Tab>('execute');

  return (
    <div className="flex flex-col h-screen bg-background">
      {/* Header */}
      <header className="border-b">
        <div className="flex items-center justify-between px-6 py-4">
          <h1 className="text-2xl font-bold">JQ Playground</h1>
          <div className="flex gap-2">
            <a
              href="https://github.com/speakeasy-api/jq"
              target="_blank"
              rel="noopener noreferrer"
              className="text-sm text-muted-foreground hover:text-foreground"
            >
              GitHub
            </a>
          </div>
        </div>
      </header>

      {/* Tab Navigation */}
      <div className="border-b">
        <nav className="flex px-6">
          <button
            onClick={() => setActiveTab('execute')}
            className={`px-4 py-2 border-b-2 transition-colors ${
              activeTab === 'execute'
                ? 'border-primary text-foreground font-medium'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            Execute
          </button>
          <button
            onClick={() => setActiveTab('symbolic')}
            className={`px-4 py-2 border-b-2 transition-colors ${
              activeTab === 'symbolic'
                ? 'border-primary text-foreground font-medium'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            Symbolic
          </button>
        </nav>
      </div>

      {/* Tab Content */}
      <div className="flex-1 overflow-hidden">
        {activeTab === 'execute' ? <ExecuteTab /> : <SymbolicTab />}
      </div>
    </div>
  );
}
