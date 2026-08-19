import { Window } from '@wailsio/runtime'

export function setupWindowControls() {
  document.getElementById('btn-min')?.addEventListener('click', () => Window.Minimise())
  document.getElementById('btn-max')?.addEventListener('click', async () => {
    const isMax = await Window.IsMaximised()
    if (isMax) await Window.Restore()
    else await Window.Maximise()
  })
  document.getElementById('btn-close')?.addEventListener('click', () => Window.Close())
}

export function updateTitlebarPort(port) {
  const el = document.getElementById('titlebar-port')
  if (el) el.textContent = 'localhost:' + port
}