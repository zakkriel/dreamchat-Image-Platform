import { ScenarioImportPanel } from './components/ScenarioImportPanel'
import { ConnectionPanel } from './components/ConnectionPanel'
import { StylePanel } from './components/StylePanel'
import { VisualIdentityPanel } from './components/VisualIdentityPanel'
import { ArtifactPanel } from './components/ArtifactPanel'
import { PackPanel } from './components/PackPanel'
import { JobMonitorPanel } from './components/JobMonitorPanel'
import { AssetSearchPanel } from './components/AssetSearchPanel'
import { WebhookPanel } from './components/WebhookPanel'
import { AdminJobPanel } from './components/AdminJobPanel'
import { RequestLogPanel } from './components/RequestLogPanel'

export default function App() {
  return (
    <div className="app">
      <header className="app-head">
        <h1>Image Platform Playground</h1>
        <p className="muted">
          Dev-only local testing console for the DreamChat Image Platform API. Not a productized admin
          dashboard.
        </p>
      </header>
      <main className="panels">
        <ScenarioImportPanel />
        <ConnectionPanel />
        <StylePanel />
        <VisualIdentityPanel />
        <ArtifactPanel />
        <PackPanel />
        <JobMonitorPanel />
        <AssetSearchPanel />
        <WebhookPanel />
        <AdminJobPanel />
        <RequestLogPanel />
      </main>
    </div>
  )
}
