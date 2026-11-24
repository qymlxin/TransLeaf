import { useState } from 'react'
import { HomePage } from './pages/HomePage'
import { WorkspacePage } from './pages/WorkspacePage'
import { ModelSettingsModal } from './components/settings/ModelSettingsModal'

export function App() {
  const [activeDocId, setActiveDocId] = useState<string | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)

  return (
    <div className="h-full">
      {activeDocId ? (
        <WorkspacePage
          docId={activeDocId}
          onBack={() => setActiveDocId(null)}
          onOpenSettings={() => setSettingsOpen(true)}
        />
      ) : (
        <HomePage
          onOpenDocument={(id) => setActiveDocId(id)}
          onOpenSettings={() => setSettingsOpen(true)}
        />
      )}

      <ModelSettingsModal open={settingsOpen} onClose={() => setSettingsOpen(false)} />
    </div>
  )
}

export default App
