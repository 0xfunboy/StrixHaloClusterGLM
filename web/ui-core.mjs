// Pure, dependency-free presentation helpers. Keep this module DOM-independent.
// Deliberately whitelist display fields: server credentials and SSH data must
// never reach browser persistence through this object.
export function displayPreferences(value = {}) {
  const source = value && typeof value === 'object' ? value : {};
  return {
    language: source.language === 'it' ? 'it' : 'en',
    text_size: [14, 16, 18].includes(Number(source.text_size)) ? Number(source.text_size) : 14,
    density: source.density === 'compact' ? 'compact' : 'comfortable',
    expand_thinking: source.expand_thinking === true,
    sidebar_collapsed: source.sidebar_collapsed === true,
    show_advanced: source.show_advanced === true,
  };
}

// Visibility only: switching sections must not rewrite a user's generation values.
export function generationPanelState(tab, showAdvanced = false) {
  const workspace = tab === 'workspace';
  const fullControls = tab === 'chat' || (tab === 'coding' && showAdvanced === true);
  return { visible: workspace || fullControls, workspace, fullControls };
}

export function apiSettings(value) {
  const names = ['chat', 'workspaces', 'legacy_coding', 'operations'];
  if (!value || !names.every(name => typeof value[name] === 'boolean')) throw new Error('The server did not provide all API settings. No changes were applied.');
  return Object.fromEntries(names.map(name => [name, value[name]]));
}

export const IT_LABELS = {
  "Options": "Opzioni",
  "Generation": "Generazione",
  "HaloClu home": "Home HaloClu",
  "Reasoning for new sessions": "Ragionamento per nuove sessioni",
  "Applied when you create a Pi session. Existing sessions keep their captured reasoning. Chat context, output and thinking-budget controls are not passed to Pi.": "Applicato quando crei una sessione Pi. Le sessioni esistenti mantengono il ragionamento acquisito alla creazione. Contesto, limite risposta e budget thinking della chat non sono passati a Pi.",
  "Show Advanced / legacy in navigation": "Mostra Advanced / legacy nella navigazione",
  "Optional snapshot/build/test fallback. Hiding the page does not disable its API or discard existing tasks.": "Flusso opzionale snapshot/build/test. Nascondere la pagina non disabilita la sua API né elimina le attività esistenti.",
  "Refresh server settings": "Aggiorna impostazioni server",
  "Interface": "Interfaccia",
  "Saved in this browser. These preferences do not change model generation.": "Salvate in questo browser. Queste preferenze non modificano la generazione del modello.",
  "Conversation text size": "Dimensione testo conversazione",
  "Standard · 14 px": "Standard · 14 px",
  "Large · 16 px": "Grande · 16 px",
  "Larger · 18 px": "Più grande · 18 px",
  "Layout density": "Densità interfaccia",
  "Comfortable": "Comoda",
  "Compact": "Compatta",
  "Thinking display only. Reasoning effort and token budgets remain under Generation.": "Solo visualizzazione del thinking. Ragionamento e budget token rimangono sotto Generazione.",
  "Connection and secrets": "Connessione e segreti",
  "Copy token": "Copia token",
  "Rotate API token…": "Ruota token API…",
  "Creates a new server token. Other clients must reconnect with it. Does not change SSH keys or passwords.": "Crea un nuovo token server. Gli altri client devono riconnettersi con esso. Non modifica chiavi SSH o password.",
  "Exposed APIs": "API esposte",
  "Connect to inspect the server's API controls.": "Connettiti per consultare i controlli API del server.",
  "Chat and tool completions": "Chat e completamenti tool",
  "Coding workspace / Pi": "Workspace coding / Pi",
  "Advanced / legacy coding": "Coding avanzato / legacy",
  "Benchmarks and download operations": "Benchmark e operazioni download",
  "Save API settings…": "Salva impostazioni API…",
  "Server settings not loaded.": "Impostazioni server non caricate.",
  "Server configuration": "Configurazione server",
  "Listening address": "Indirizzo in ascolto",
  "Active model": "Modello attivo",
  "Network binding and backend are managed by the server configuration. Changing them requires a gateway restart, not a rank restart.": "Indirizzo di rete e backend sono gestiti dalla configurazione server. Per cambiarli serve riavviare il gateway, non un rank.",
  "Skip to conversation": "Vai alla conversazione",
  "Navigation and settings": "Navigazione e impostazioni",
  "Collapse sidebar": "Comprimi sidebar",
  "Sections": "Sezioni",
  "Models": "Modelli",
  "Request settings": "Parametri richiesta",
  "Settings": "Impostazioni",
  "Low does not disable thinking.": "Low non disabilita il thinking.",
  "Thinking budget": "Budget thinking",
  "Natural · no added cap": "Naturale · nessun cap aggiunto",
  "0 · force reasoning closure": "0 · chiusura forzata del ragionamento",
  "A cap changes generation; equivalent quality is not guaranteed.": "Un cap cambia la generazione; qualità equivalente non garantita.",
  "Context window": "Finestra contesto",
  "Chat: input + output. Legacy coding: source-selection budget. Does not resize KV.": "Chat: input + output. Coding: budget selezione sorgenti. Non ridimensiona la KV.",
  "Response limit (includes thinking)": "Limite risposta (include thinking)",
  "Waiting for server settings.": "Parametri in attesa del server.",
  "Expand thinking while responding": "Espandi thinking durante la risposta",
  "Settings take effect when you submit a request.": "Nessuna modifica al modello finché non invii una richiesta.",
  "Local conversations": "Conversazioni locali",
  "Conversations": "Conversazioni",
  "New conversation": "Nuova conversazione",
  "Only in this tab. Export to keep a copy.": "Solo in questa scheda. Nessun salvataggio automatico su disco.",
  "Not connected": "Non connesso",
  "Toggle sidebar": "Mostra o nascondi sidebar",
  "Waiting for connection": "Connessione in attesa",
  "Token and connection": "Token e connessione",
  "Refresh status": "Aggiorna stato",
  "API authentication": "Autenticazione API",
  "Local API token": "Token API locale",
  "Kept only in this tab's memory.": "Resta soltanto nella memoria di questa scheda.",
  "Local token": "Token locale",
  "Connect": "Connetti",
  "Forget": "Dimentica",
  "No external services.": "Nessun servizio esterno.",
  "Conversation": "Conversazione",
  "Send a message. For repository work, open": "Scrivi un messaggio. Per modifiche al repository con build e test, usa",
  "Response metrics": "Metriche risposta",
  "Observed TPS": "TPS osservati",
  "Final decode": "Decode finale",
  "HTTP TPS": "TPS HTTP",
  "Decode TPS": "TPS decode",
  "Draft acceptance": "Accettazione draft",
  "Tokens/step": "Token/passo",
  "Decode excludes prefill; HTTP includes it. Live decode is browser-observed; final decode comes from the engine. Both include thinking tokens.": "Decode esclude il prefill; HTTP lo include. Il decode live è osservato dal browser; quello finale proviene dal motore. Entrambi includono i token di thinking.",
  "Context": "Contesto",
  "Message": "Messaggio",
  "Write a message…": "Scrivi un messaggio…",
  "Stop stream": "Interrompi stream",
  "Send": "Invia",
  "Observed TPS: cumulative real tokens / HTTP time, thinking included. Final decode: engine metric. A cap or timeout is not a completed answer.": "TPS osservati: token reali cumulativi / tempo HTTP, incluso thinking. Decode finale: metrica del motore. Un cap o timeout non è una risposta conclusa.",
  "Advanced fallback: isolated snapshot → build → test → repair. Apply to the original only after confirmation.": "Snapshot isolato → build → test → correzione. Applicazione all'originale soltanto su conferma.",
  "Import task JSON": "Importa task JSON",
  "Clear imported options": "Rimuovi opzioni importate",
  "Importing does not submit a task or apply changes.": "Importare non avvia il task e non applica modifiche.",
  "Instruction": "Istruzione",
  "Describe the problem and expected outcome.": "Descrivi il problema e il risultato atteso.",
  "/absolute/path/project": "/percorso/assoluto/progetto",
  "Editable files": "File modificabili",
  "relative paths, one per line": "percorsi relativi, uno per riga",
  "Build command": "Comando build",
  "optional": "opzionale",
  "Test command": "Comando test",
  "Request timeout (s)": "Timeout richiesta (s)",
  "Maximum repairs": "Massimo repair",
  "Reasoning and output limits follow the sidebar; legacy context is a source-selection budget. The runtime also enforces the total limit. Commands run only in the sandbox.": "Reasoning e limite risposta seguono la sidebar; nel coding il contesto è il budget di selezione sorgenti. Il runtime applica anche il limite totale. Comandi solo nella sandbox.",
  "Start isolated task": "Avvia task isolato",
  "Execution": "Esecuzione",
  "Existing task ID": "ID task esistente",
  "Open": "Apri",
  "No task submitted": "Nessun task inviato",
  "Total time": "Tempo totale",
  "Model calls": "Chiamate modello",
  "Last attempt TPS": "TPS ultimo tentativo",
  "No results.": "Nessun risultato.",
  "Cancel task": "Annulla task",
  "Refresh": "Aggiorna",
  "Apply passing patch": "Applica patch PASS",
  "Copy": "Copia",
  "Save": "Salva",
  "No changes.": "Nessuna modifica.",
  "The diff will appear here. Generation does not modify the original repository.": "Il diff comparirà qui. Il repository originale non viene modificato dalla generazione.",
  "Task details and metrics": "Dettagli task e metriche",
  "Refresh catalog": "Aggiorna catalogo",
  "Local availability, runtime, distribution and limits come from the server catalog. Models never load automatically.": "Disponibilità locale, runtime, distribuzione e limiti provengono dal catalogo del server. Nessun modello viene caricato automaticamente.",
  "Open the catalog to inspect local availability.": "Apri il catalogo per leggere lo stato locale.",
  "Benchmarks and operations": "Benchmark e operazioni",
  "Every run or download requires confirmation. Historical results are not measurements from this session.": "Ogni esecuzione o download richiede una conferma. I risultati storici non rappresentano misure della sessione corrente.",
  "Available actions": "Azioni disponibili",
  "Options not loaded.": "Opzioni non ancora caricate.",
  "No data.": "Nessun dato.",
  "Recorded evidence": "Evidenze registrate",
  "Catalog not loaded.": "Catalogo non ancora caricato.",
  "Model not detected": "Modello non ancora rilevato",
  "Runtime details unavailable.": "Dettagli runtime non disponibili.",
  "Node telemetry not received.": "Telemetria dei nodi non ancora ricevuta.",
  "Last available measurement": "Ultima misura disponibile",
  "Active request": "Richiesta attiva",
  "Server-reported state": "Stato riportato dal server",
  "Runtime limit": "Limite runtime",
  "Not a quality qualification at this context length": "Non è una qualifica di qualità al contesto indicato",
  "Last update": "Ultimo aggiornamento",
  "No response received": "Nessuna risposta ricevuta",
  "Pair lifecycle remains under the ownership-aware controller. The UI never restarts one rank alone.": "Il lifecycle della coppia resta nel controller ownership-aware. Nessun riavvio isolato di un rank dalla UI.",
  "Raw health and status": "Health e stato grezzo"
};
Object.assign(IT_LABELS, {
  'Language': 'Lingua', 'Coding workspace': 'Workspace di coding', 'Advanced / legacy': 'Avanzato / legacy', 'Advanced legacy coding': 'Coding avanzato legacy',
  'Export JSON': 'Esporta JSON', 'Attach files': 'Allega file', 'Ctrl / ⌘ + Enter': 'Ctrl / ⌘ + Invio',
  'Text, code, PDF or binary inspection · up to 8 files / 32 MiB each': 'Testo, codice, PDF o ispezione binaria · fino a 8 file / 32 MiB ciascuno',
  'Checking workspace capabilities requires an authenticated connection.': 'Per verificare il workspace serve una connessione autenticata.',
  'Connection': 'Connessione', 'Location': 'Posizione', 'Local': 'Locale', 'Remote SSH': 'Remoto SSH', 'Workspace root': 'Radice workspace',
  'SSH preset': 'Preset SSH', 'Choose a preset': 'Scegli un preset', 'Password (optional, this request only)': 'Password (opzionale, solo questa richiesta)',
  'Existing session': 'Sessione esistente', 'No session selected': 'Nessuna sessione selezionata', 'Create session': 'Crea sessione', 'Start Pi': 'Avvia Pi',
  'New SSH preset': 'Nuovo preset SSH', 'Name': 'Nome', 'Port': 'Porta', 'User': 'Utente', 'Private key path (optional)': 'Percorso chiave privata (opzionale)',
  'Remote root': 'Radice remota', 'Save preset': 'Salva preset', 'Host identity must already be trusted by the server. Passwords are not saved in presets.': 'Identità host già fidata dal server. Le password non vengono salvate nei preset.',
  'Abort Pi': 'Interrompi Pi', 'Close session': 'Chiudi sessione', 'No active workspace.': 'Nessun workspace attivo.', 'Pi conversation': 'Conversazione Pi',
  'Agent tools act inside the connected workspace. Review the workspace and capability status before submitting. This is separate from plain chat.': 'Gli strumenti agentici agiscono nel workspace connesso. Verifica destinazione e capacità prima di inviare. È separato dalla chat semplice.',
  'Describe the repository task…': 'Descrivi il task sul repository…', 'Send to Pi': 'Invia a Pi', 'Agent events / tools': 'Eventi agente / strumenti',
  'No events.': 'Nessun evento.', 'Files': 'File', 'Relative directory': 'Cartella relativa', 'List': 'Elenca', 'Choose a file to inspect.': 'Scegli un file da ispezionare.',
  'Terminal diagnostics': 'Diagnostica terminale', 'Terminal': 'Terminale', 'Diagnostic preset': 'Preset diagnostico', 'Allowed command': 'Comando consentito', 'Run': 'Esegui', 'No command executed.': 'Nessun comando eseguito.',
  'Custom shell command': 'Comando shell personalizzato', 'Enter a command for the connected workspace…': 'Inserisci un comando per il workspace connesso…', 'Run command…': 'Esegui comando…',
  'Custom commands require an idle Pi session and explicit confirmation. Local: direct writes inside the workspace sandbox, no deferred Apply. SSH: commands use the remote account privileges.': 'I comandi richiedono una sessione Pi inattiva e conferma esplicita. Locale: scritture dirette nella sandbox workspace, senza Applica differito. SSH: privilegi dell’account remoto.',
  'Execution and output are recorded in Pi events; this is not an interactive PTY.': 'Esecuzione e output sono registrati negli eventi Pi; non è una PTY interattiva.',
  'Only server-listed diagnostic commands. Agent tool execution is recorded in Pi events.': 'Solo comandi diagnostici dichiarati dal server. Gli strumenti dell’agente sono registrati negli eventi Pi.'
});

export const TERMINAL_STATES = new Set([
  'passed', 'pass', 'success', 'succeeded', 'completed', 'failed', 'fail', 'error',
  'cancelled', 'canceled', 'incomplete', 'timeout', 'applied', 'blocked', 'interrupted',
]);

export function isTerminal(status) {
  return TERMINAL_STATES.has(String(status || '').toLowerCase());
}

export function isSuccess(status) {
  return ['passed', 'pass', 'success', 'succeeded', 'applied'].includes(String(status || '').toLowerCase());
}

export function canCancelTask(status) {
  return ['queued', 'running'].includes(String(status || '').toLowerCase());
}

export function canRunWorkspaceShell(session) {
  return String(session?.state || session?.status || '').toUpperCase() === 'READY' && session?.capabilities?.shell === true;
}

export function classifyStatus(status) {
  const value = String(status || '').toLowerCase();
  if (isSuccess(value) || ['ok', 'healthy', 'ready', 'online'].includes(value)) return 'good';
  if (['failed', 'fail', 'error', 'poisoned', 'unhealthy', 'offline', 'blocked'].includes(value)) return 'bad';
  return 'neutral';
}

export function healthStatus(value, fallback = 'unknown') {
  if (typeof value === 'string') return value;
  if (typeof value?.status === 'string') return value.status;
  if (value?.ok === true) return 'ok';
  if (value?.ok === false) return 'unhealthy';
  return fallback;
}

export function activeRequestLabel(value, busy) {
  if (Array.isArray(value)) return value.length ? value.join(' · ') : busy === true ? 'Active' : 'Idle';
  if (value === true || busy === true) return 'Active';
  if (value === false || value === null || busy === false) return 'Idle';
  if (typeof value === 'string') return value;
  if (value?.id) return String(value.id);
  return '—';
}

export function lifecycleControlState(value, busy = false) {
  const state = String(value?.state || 'ERROR').toUpperCase();
  const owner = String(value?.cluster_owner || value?.owner || 'UNKNOWN').toUpperCase();
  const coordinator = String(value?.coordinator || 'OFF').toUpperCase();
  const nodes = Array.isArray(value?.nodes) ? value.nodes : [];
  const active = state === 'READY' || coordinator === 'RUNNING' || nodes.some(node => String(node?.engine_state || '').toUpperCase() === 'ACTIVE');
  const startAllowed = value?.start_allowed === true || (value?.managed === true && state === 'OFF' && owner === 'NONE' && !('start_allowed' in value));
  const transitional = state === 'STARTING' || state === 'STOPPING';
  return {
    state,
    owner,
    coordinator,
    readiness: String(value?.readiness || 'NOT_READY'),
    detail: String(value?.detail || value?.last_error || ''),
    drainSeconds: finite(value?.drain_deadline_seconds),
    nodes,
    poll: transitional,
    onDisabled: Boolean(busy || transitional || state === 'READY' || !startAllowed),
    offDisabled: Boolean(busy || transitional || !active),
  };
}

export function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, character => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[character]);
}

export function finite(value) {
  if (value === null || value === undefined || value === '' || typeof value === 'boolean') return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

export function number(value, digits = 1) {
  const result = finite(value);
  return result === null ? '—' : result.toLocaleString('en-GB', { maximumFractionDigits: digits });
}

export function seconds(value) {
  const result = finite(value);
  if (result === null) return '—';
  if (result < 1) return `${Math.round(result * 1000)} ms`;
  if (result < 60) return `${number(result, 2)} s`;
  return `${Math.floor(result / 60)}m ${Math.round(result % 60)}s`;
}

export function bytes(value) {
  const result = finite(value);
  if (result === null) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let scaled = result;
  let unit = 0;
  while (Math.abs(scaled) >= 1024 && unit < units.length - 1) { scaled /= 1024; unit++; }
  return `${number(scaled, 1)} ${units[unit]}`;
}

export function percent(value) {
  const result = finite(value);
  return result === null ? '—' : `${number(result * 100, 1)}%`;
}

export function pathList(value) {
  return [...new Set(String(value || '').split(/[\n,]/).map(path => path.trim()).filter(Boolean))];
}

export function commandText(value) {
  if (typeof value === 'string') return value;
  if (!Array.isArray(value) || value.some(part => typeof part !== 'string')) throw new Error('Commands must be a string or an array of strings.');
  return value.map(part => /^[a-zA-Z0-9_./:=+-]+$/.test(part) ? part : `'${part.replaceAll("'", "'\\''")}'`).join(' ');
}

export function importedTaskSpec(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Task JSON must be an object.');
  if (typeof value.task !== 'string' || !value.task.trim()) throw new Error('Task JSON requires a task instruction.');
  if (typeof value.repo !== 'string' || !value.repo.startsWith('/')) throw new Error('Task JSON requires an absolute repository path.');
  const stringList = (input, label) => {
    if (!Array.isArray(input) || input.some(part => typeof part !== 'string' || !part.trim())) throw new Error(`${label} must be an array of nonempty strings.`);
    return input;
  };
  const modes = ['low', 'high', 'max'];
  const legacy = ['fast', 'balanced', 'quality'];
  if (value.reasoning_effort !== undefined && !modes.includes(value.reasoning_effort)) throw new Error('Invalid reasoning_effort: use low, high or max.');
  if (value.profile !== undefined && ![...modes, ...legacy].includes(value.profile)) throw new Error('Invalid profile in task JSON.');
  if (value.reasoning_effort !== undefined && modes.includes(value.profile) && value.reasoning_effort !== value.profile) throw new Error('Conflicting explicit reasoning_effort and profile.');
  const result = {
    task: value.task, repo: value.repo,
    allowed_paths: stringList(value.allowed_paths, 'allowed_paths'),
    test_command: commandText(value.test_command),
    build_command: value.build_command ? commandText(value.build_command) : '',
    reasoning_effort: value.reasoning_effort ?? (modes.includes(value.profile) ? value.profile : 'low'),
    timeout: value.timeout ?? 300, max_repairs: value.max_repairs ?? 2,
  };
  if (legacy.includes(value.profile)) result.legacy_profile = value.profile;
  if (!result.allowed_paths.length || !result.test_command) throw new Error('Allowed source paths and a test command are required.');
  for (const [field, minimum, maximum] of [['timeout', 10, 1800], ['max_repairs', 0, 6], ['context_tokens', 256, 65536], ['max_tokens', 64, 32768]]) {
    const item = value[field] ?? result[field];
    if (item === undefined) continue;
    if (!Number.isSafeInteger(item) || item < minimum || item > maximum) throw new Error(`Invalid ${field} in task JSON.`);
    result[field] = item;
  }
  if (value.thinking_token_budget !== undefined) {
    const budget = value.thinking_token_budget;
    if (!Number.isSafeInteger(budget) || budget < 0 || budget >= (result.max_tokens ?? 32768)) throw new Error('Invalid thinking_token_budget: must be nonnegative and smaller than the output cap.');
    result.thinking_token_budget = budget;
  }
  if (value.files !== undefined) result.files = stringList(value.files, 'files');
  if (value.test_files !== undefined) {
    if (!value.test_files || typeof value.test_files !== 'object' || Array.isArray(value.test_files)) throw new Error('test_files must map sandbox paths to absolute fixture paths.');
    for (const [name, path] of Object.entries(value.test_files)) {
      if (!name || typeof path !== 'string' || !path.startsWith('/')) throw new Error('Invalid isolated test fixture path.');
    }
    result.test_files = value.test_files;
  }
  // Deliberately do not import apply, auth, endpoint, sandbox policy, or tools.
  return result;
}

// SSE permits CR, LF, CRLF, multiple data lines, comments and arbitrary byte
// boundaries. The caller streams through TextDecoder before passing strings here.
export class SSEParser {
  constructor(onEvent, maxBufferedCharacters = 2 * 1024 * 1024) {
    this.onEvent = onEvent;
    this.limit = maxBufferedCharacters;
    this.line = '';
    this.data = [];
    this.event = '';
    this.id = '';
    this.pendingCR = false;
    this.size = 0;
    this.first = true;
  }

  push(chunk) {
    for (const character of chunk) {
      if (this.first) {
        this.first = false;
        if (character === '\uFEFF') continue;
      }
      if (this.pendingCR) {
        this.pendingCR = false;
        if (character === '\n') continue;
      }
      if (character === '\r' || character === '\n') {
        this.consumeLine();
        this.pendingCR = character === '\r';
      } else {
        this.line += character;
        if (++this.size > this.limit) throw new Error('SSE event exceeded the safe size limit.');
      }
    }
  }

  consumeLine() {
    const line = this.line;
    this.line = '';
    if (!line) {
      if (this.data.length) {
        this.onEvent({ event: this.event || 'message', data: this.data.join('\n'), id: this.id });
      }
      this.data = [];
      this.event = '';
      this.size = 0;
      return;
    }
    if (line.startsWith(':')) return;
    const separator = line.indexOf(':');
    const field = separator < 0 ? line : line.slice(0, separator);
    let value = separator < 0 ? '' : line.slice(separator + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'data') this.data.push(value);
    if (field === 'event') this.event = value;
    if (field === 'id' && !value.includes('\0')) this.id = value;
  }

  finish() {
    // An unterminated event is discarded, as required by the SSE protocol.
    this.line = '';
    this.data = [];
    this.event = '';
    this.size = 0;
  }
}

export function completionDelta(payload) {
  const choice = payload?.choices?.[0];
  const delta = choice?.delta || choice?.message || {};
  return {
    content: typeof delta.content === 'string' ? delta.content : '',
    reasoning: typeof delta.reasoning_content === 'string' ? delta.reasoning_content
      : (typeof delta.reasoning === 'string' ? delta.reasoning : ''),
    finish: choice?.finish_reason || null,
    usage: payload?.usage || null,
    timings: payload?.timings || payload?.metrics || null,
  };
}

export function decodeRate(timings, usage) {
  const direct = finite(timings?.decode_tps ?? timings?.predicted_per_second);
  if (direct !== null) return direct;
  const generationMS = finite(timings?.generation_time_ms);
  const tokens = finite(usage?.completion_tokens);
  return generationMS > 0 && tokens > 1 ? (tokens - 1) * 1000 / generationMS : null;
}

// Only cumulative server token counts are measurements. A chunk is not a token.
export function observedRate(usage, elapsedSeconds) {
  const tokens = usage?.completion_tokens;
  return Number.isSafeInteger(tokens) && tokens >= 0 && elapsedSeconds > 0 ? tokens / elapsedSeconds : null;
}

// Browser-observed decode rate, not a per-kernel measurement. Remove prefill
// from the denominator; use only server token counts, never chunks or text.
export function liveDecodeRate(usage, firstTokenMS, elapsedMS) {
  const tokens = usage?.completion_tokens;
  return Number.isSafeInteger(tokens) && tokens > 1
    && Number.isFinite(firstTokenMS) && firstTokenMS >= 0
    && Number.isFinite(elapsedMS) && elapsedMS - firstTokenMS >= 250
    ? (tokens - 1) * 1000 / (elapsedMS - firstTokenMS) : null;
}

export function draftStats(metrics) {
  const spec = (metrics?.raw || metrics)?.speculative_decoding;
  const acceptance = finite(spec?.draft_acceptance_rate ?? metrics?.acceptance);
  const length = finite(spec?.mean_acceptance_length);
  return {
    acceptance: acceptance !== null && acceptance >= 0 && acceptance <= 1 ? acceptance : null,
    length: length !== null && length >= 1 ? length : null,
  };
}

export function generationSettings(reasoning, context, output, options) {
  if (!options?.reasoning_modes?.includes(reasoning)) throw new Error('Reasoning mode is not supported by this server.');
  const contextTokens = Number(context);
  if (!options.context_options?.includes(contextTokens)) throw new Error('Select a supported total context window.');
  const settings = { reasoning_effort: reasoning, context_tokens: contextTokens };
  if (output !== '') {
    const maximum = Number(output);
    if (!Number.isSafeInteger(maximum) || maximum < 32 || maximum > options.max_output_tokens) throw new Error('Response limit exceeds the server-supported range.');
    settings.max_tokens = maximum;
  }
  return settings;
}

export function safeSourceURL(value) {
  try {
    const url = new URL(value);
    return ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password ? url.href : null;
  } catch { return null; }
}

// Preserve every character while marking fenced code. Rendering uses textContent.
export function textBlocks(value) {
  const text = String(value);
  const pattern = /(^|\n)(```[^\n]*\n[\s\S]*?\n```(?=\n|$))/g;
  const blocks = [];
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    const start = match.index + match[1].length;
    if (start > cursor) blocks.push({ code: false, text: text.slice(cursor, start) });
    blocks.push({ code: true, text: match[2] });
    cursor = start + match[2].length;
  }
  if (cursor < text.length || !blocks.length) blocks.push({ code: false, text: text.slice(cursor) });
  return blocks;
}

// Small, dependency-free Markdown AST. Raw HTML stays literal text. Parsing is
// bounded and never evaluates model output, URLs, attributes or language names.
export function markdownBlocks(value) {
  const lines = String(value).replace(/\r\n?/g, '\n').split('\n');
  const blocks = [];
  const tableCells = line => line.trim().replace(/^\||\|$/g, '').split(/(?<!\\)\|/).map(cell => cell.trim().replace(/\\\|/g, '|'));
  const special = line => /^\s*$|^ {0,3}(?:`{3,}|~{3,}|#{1,6}\s|>\s?|[-*+]\s|\d+[.)]\s|(?:[-*_]\s*){3,}$)/.test(line);
  for (let i = 0; i < lines.length;) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }
    const fence = line.match(/^ {0,3}(`{3,}|~{3,})([^`]*)$/);
    if (fence) {
      const marker = fence[1][0], length = fence[1].length, body = [];
      i++;
      while (i < lines.length && !(new RegExp(`^ {0,3}${marker === '`' ? '`' : '~'}{${length},}\\s*$`)).test(lines[i])) body.push(lines[i++]);
      if (i < lines.length) i++;
      blocks.push({ type: 'code', language: fence[2].trim().split(/\s+/)[0].slice(0, 40), text: body.join('\n') });
      continue;
    }
    const heading = line.match(/^ {0,3}(#{1,6})\s+(.+?)\s*#*$/);
    if (heading) { blocks.push({ type: 'heading', level: heading[1].length, text: heading[2] }); i++; continue; }
    if (/^ {0,3}(?:[-*_]\s*){3,}$/.test(line)) { blocks.push({ type: 'rule' }); i++; continue; }
    if (/^ {0,3}>/.test(line)) {
      const quoted = [];
      while (i < lines.length && /^ {0,3}>/.test(lines[i])) quoted.push(lines[i++].replace(/^ {0,3}>\s?/, ''));
      blocks.push({ type: 'quote', text: quoted.join('\n') }); continue;
    }
    if (line.includes('|') && i + 1 < lines.length && tableCells(lines[i + 1]).every(cell => /^:?-{3,}:?$/.test(cell))) {
      const header = tableCells(line), rows = [];
      i += 2;
      while (i < lines.length && lines[i].includes('|') && lines[i].trim()) rows.push(tableCells(lines[i++]));
      blocks.push({ type: 'table', header, rows }); continue;
    }
    const list = line.match(/^ {0,3}([-*+]|\d+[.)])\s+(.+)$/);
    if (list) {
      const ordered = /^\d/.test(list[1]), items = [];
      while (i < lines.length) {
        const item = lines[i].match(/^ {0,3}([-*+]|\d+[.)])\s+(.+)$/);
        if (!item || /^\d/.test(item[1]) !== ordered) break;
        let text = item[2]; i++;
        while (i < lines.length && /^ {2,}\S/.test(lines[i]) && !/^\s*([-*+]|\d+[.)])\s/.test(lines[i])) text += '\n' + lines[i++].trim();
        items.push(text);
      }
      blocks.push({ type: 'list', ordered, start: ordered ? parseInt(list[1], 10) : 1, items }); continue;
    }
    const paragraph = [line]; i++;
    while (i < lines.length && !special(lines[i]) && !(lines[i].includes('|') && i + 1 < lines.length && tableCells(lines[i + 1]).every(cell => /^:?-{3,}:?$/.test(cell)))) paragraph.push(lines[i++]);
    blocks.push({ type: 'paragraph', text: paragraph.join('\n') });
  }
  return blocks;
}

export function markdownInline(value) {
  const text = String(value), result = [];
  const pattern = /(`+)([^`]+?)\1|\*\*([^*]+)\*\*|__([^_]+)__|(?<!\*)\*([^*\n]+)\*(?!\*)|\[([^\]\n]+)\]\(([^\s)]+)\)/g;
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > cursor) result.push({ type: 'text', text: text.slice(cursor, match.index) });
    if (match[1]) result.push({ type: 'code', text: match[2] });
    else if (match[3] || match[4]) result.push({ type: 'strong', text: match[3] || match[4] });
    else if (match[5]) result.push({ type: 'em', text: match[5] });
    else {
      const href = safeSourceURL(match[7]);
      result.push(href ? { type: 'link', text: match[6], href } : { type: 'text', text: match[0] });
    }
    cursor = match.index + match[0].length;
  }
  if (cursor < text.length) result.push({ type: 'text', text: text.slice(cursor) });
  return result;
}

export function completionState(done, finish, content) {
  if (done !== true) return 'incomplete-stream';
  if (finish === 'length') return 'incomplete-cap';
  if (finish !== 'stop' || typeof content !== 'string' || !content.trim()) return 'no-complete-final';
  return 'complete';
}

export function errorMessage(payload, fallback = 'Request failed.') {
  if (typeof payload === 'string' && payload) return payload.slice(0, 1600);
  const message = payload?.error?.message || payload?.message || payload?.error;
  return typeof message === 'string' ? message.slice(0, 1600) : fallback;
}

export function normalizeTask(payload) {
  const task = payload?.task && typeof payload.task === 'object' ? payload.task : payload || {};
  const result = task.result && typeof task.result === 'object' ? task.result : {};
  const merged = { ...task, ...result };
  return {
    ...merged,
    id: task.id || task.task_id || merged.id || '',
    status: task.status || merged.status || 'unknown',
    profile: merged.profile || merged.reasoning_profile || merged.profile_used || '',
    attempts: Array.isArray(merged.attempts) ? merged.attempts : [],
    files_changed: Array.isArray(merged.files_changed) ? merged.files_changed
      : (merged.files && typeof merged.files === 'object' ? Object.keys(merged.files) : []),
    metrics: merged.metrics || {},
    diff: typeof merged.diff === 'string' ? merged.diff : (typeof merged.patch === 'string' ? merged.patch : ''),
  };
}
