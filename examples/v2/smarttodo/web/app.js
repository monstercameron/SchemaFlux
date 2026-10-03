const SESSION_API_KEY = "smarttodo-demo:api-key";
const LOCAL_API_KEY = "smarttodo-demo:api-key:persisted";
const CONTEXT_KEY = "smarttodo-demo:context";

const els = {
  wasmStatus: document.getElementById("wasm-status"),
  keyGate: document.getElementById("key-gate"),
  keyInput: document.getElementById("api-key-input"),
  rememberKey: document.getElementById("remember-key"),
  keyStatus: document.getElementById("key-status"),
  connectKey: document.getElementById("connect-key"),
  forgetKey: document.getElementById("forget-key"),
  contextInput: document.getElementById("context-input"),
  terminalRoot: document.getElementById("terminal-root"),
};

let term = null;
let fitAddon = null;
let canvasAddon = null;
let runtimeReady = false;
let tuiStarted = false;
let termWriteQueue = Promise.resolve();
let stdoutSampleCount = 0;
const MAX_STDOUT_SAMPLES = 8;

boot().catch((error) => {
  els.wasmStatus.textContent = `Boot failed: ${error.message}`;
  setKeyStatus(`Boot failed: ${error.message}`, true);
});

async function boot() {
  await waitForTerminalFont();
  initTerminal();
  bindControls();
  restoreSavedFields();
  await bootWasm();
  await callWasm("boot");
  els.wasmStatus.textContent = "Ready";
}

function initTerminal() {
  term = new window.Terminal({
    convertEol: false,
    cursorBlink: true,
    fontFamily: '"Cascadia Mono", "Cascadia Code", Consolas, monospace',
    fontSize: 14,
    lineHeight: 1,
    letterSpacing: 0,
    fontWeight: 400,
    scrollback: 0,
    theme: {
      background: "#000000",
      foreground: "#dbe7f3",
      cursor: "#67e8f9",
      black: "#000000",
      brightBlack: "#64748b",
      red: "#fda4af",
      brightRed: "#fecdd3",
      green: "#86efac",
      brightGreen: "#bbf7d0",
      yellow: "#fcd34d",
      brightYellow: "#fde68a",
      blue: "#93c5fd",
      brightBlue: "#bfdbfe",
      magenta: "#c4b5fd",
      brightMagenta: "#ddd6fe",
      cyan: "#67e8f9",
      brightCyan: "#a5f3fc",
      white: "#e5e7eb",
      brightWhite: "#ffffff",
    },
  });

  fitAddon = new window.FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  if (window.CanvasAddon?.CanvasAddon) {
    canvasAddon = new window.CanvasAddon.CanvasAddon();
    term.loadAddon(canvasAddon);
  }
  term.open(els.terminalRoot);
  fitAddon.fit();
  requestAnimationFrame(() => {
    fitAddon.fit();
    if (tuiStarted && window.smarttodoWasm?.resize) {
      window.smarttodoWasm.resize(term.cols, term.rows);
    }
  });
  term.focus();

  term.onData((data) => {
    if (tuiStarted && window.smarttodoWasm?.feed) {
      window.smarttodoWasm.feed(data);
    }
  });

  window.addEventListener("resize", handleResize);
  if (window.ResizeObserver) {
    const observer = new ResizeObserver(() => handleResize());
    observer.observe(els.terminalRoot);
  }
}

function bindControls() {
  els.connectKey.addEventListener("click", () => void startTUI());
  els.forgetKey.addEventListener("click", forgetKey);
  els.contextInput.addEventListener("input", () => {
    localStorage.setItem(CONTEXT_KEY, els.contextInput.value.trim());
  });
}

function restoreSavedFields() {
  const savedKey = localStorage.getItem(LOCAL_API_KEY) || sessionStorage.getItem(SESSION_API_KEY);
  if (savedKey) {
    els.keyInput.value = savedKey;
    els.rememberKey.checked = Boolean(localStorage.getItem(LOCAL_API_KEY));
  }
  const savedContext = localStorage.getItem(CONTEXT_KEY);
  if (savedContext) {
    els.contextInput.value = savedContext;
  }
}

async function bootWasm() {
  const go = new Go();
  const response = await fetch("smarttodo.wasm");
  if (!response.ok) {
    throw new Error(`WASM fetch failed with ${response.status}`);
  }

  const decoder = new TextDecoder();
  globalThis.fs.writeSync = (fd, buf) => {
    const text = decoder.decode(buf, { stream: true });
    if (text) {
      if (stdoutSampleCount < MAX_STDOUT_SAMPLES) {
        console.debug("[smarttodo-host] stdout", {
          fd,
          bytes: buf.length,
          preview: previewTerminalChunk(text),
        });
        stdoutSampleCount += 1;
      }
      queueTerminalWrite(text);
    }
    return buf.length;
  };

  const bytes = await response.arrayBuffer();
  const result = await WebAssembly.instantiate(bytes, go.importObject);
  go.run(result.instance);

  const started = performance.now();
  while (!window.smarttodoWasm) {
    if (performance.now() - started > 5000) {
      throw new Error("smarttodo WASM API did not initialize");
    }
    await sleep(40);
  }
  runtimeReady = true;
}

async function startTUI() {
  if (!runtimeReady) {
    setKeyStatus("WASM runtime is still loading.", true);
    return;
  }

  const apiKey = els.keyInput.value.trim();
  if (!apiKey) {
    setKeyStatus("API key is required.", true);
    return;
  }

  try {
    persistKey(apiKey);
    term.reset();
    await callWasm("connect", apiKey, term.cols, term.rows);
    tuiStarted = true;
    els.keyGate.classList.add("hidden");
    setKeyStatus("Connected.", false);
    handleResize();
    term.focus();
  } catch (error) {
    setKeyStatus(`Connection failed: ${error.message}`, true);
  }
}

function persistKey(apiKey) {
  if (els.rememberKey.checked) {
    localStorage.setItem(LOCAL_API_KEY, apiKey);
    sessionStorage.removeItem(SESSION_API_KEY);
  } else {
    sessionStorage.setItem(SESSION_API_KEY, apiKey);
    localStorage.removeItem(LOCAL_API_KEY);
  }
}

function forgetKey() {
  els.keyInput.value = "";
  sessionStorage.removeItem(SESSION_API_KEY);
  localStorage.removeItem(LOCAL_API_KEY);
  setKeyStatus("Stored key removed.", false);
}

function handleResize() {
  fitAddon.fit();
  if (tuiStarted && window.smarttodoWasm?.resize) {
    window.smarttodoWasm.resize(term.cols, term.rows);
  }
}

async function callWasm(method, ...args) {
  const raw = await window.smarttodoWasm[method](...args);
  return JSON.parse(raw);
}

function setKeyStatus(message, isError) {
  els.keyStatus.textContent = message;
  els.keyStatus.style.color = isError ? "#fecaca" : "#8aa0b8";
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForTerminalFont() {
  if (!document.fonts?.ready) {
    return;
  }
  try {
    await document.fonts.ready;
  } catch {
    // Continue with terminal boot if the browser font API fails.
  }
}

function queueTerminalWrite(text) {
  termWriteQueue = termWriteQueue
    .then(() => new Promise((resolve) => term.write(text, resolve)))
    .catch(() => undefined);
}

function previewTerminalChunk(text) {
  return text
    .replace(/\u001b/g, "\\u001b")
    .replace(/\r/g, "\\r")
    .replace(/\n/g, "\\n")
    .slice(0, 180);
}
