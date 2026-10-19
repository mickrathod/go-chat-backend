(function () {
  // Read configuration from script tag
  const currentScript = document.currentScript;
  const appId = currentScript?.getAttribute("data-app-id") || "demo_app";
  const primaryColor = currentScript?.getAttribute("data-color") || "#00add8";
  const host = currentScript?.src ? new URL(currentScript.src).host : "localhost:8080";

  // Create container
  const container = document.createElement("div");
  container.id = "go-chat-sdk-container";
  document.body.appendChild(container);

  // Inject Styles
  const style = document.createElement("style");
  style.textContent = `
    #go-chat-bubble {
      position: fixed;
      bottom: 24px;
      right: 24px;
      width: 60px;
      height: 60px;
      border-radius: 50%;
      background: ${primaryColor};
      color: white;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 28px;
      cursor: pointer;
      box-shadow: 0 10px 25px rgba(0,0,0,0.3);
      transition: all 0.25s cubic-bezier(0.175, 0.885, 0.32, 1.275);
      z-index: 999999;
      user-select: none;
    }
    #go-chat-bubble:hover {
      transform: scale(1.08);
      box-shadow: 0 15px 35px rgba(0,0,0,0.4);
    }
    #go-chat-window {
      position: fixed;
      bottom: 96px;
      right: 24px;
      width: 360px;
      height: 520px;
      background: #0f141f;
      border: 1px solid rgba(255,255,255,0.1);
      border-radius: 20px;
      box-shadow: 0 20px 50px rgba(0,0,0,0.5);
      display: none;
      flex-direction: column;
      overflow: hidden;
      z-index: 999999;
      font-family: system-ui, -apple-system, sans-serif;
      animation: goChatPopup 0.25s ease-out;
    }
    @keyframes goChatPopup {
      from { opacity: 0; transform: translateY(20px) scale(0.95); }
      to { opacity: 1; transform: translateY(0) scale(1); }
    }
    .go-chat-header {
      background: ${primaryColor};
      padding: 16px;
      color: white;
      display: flex;
      align-items: center;
      justify-content: space-between;
    }
    .go-chat-title {
      font-weight: 700;
      font-size: 15px;
    }
    .go-chat-subtitle {
      font-size: 11px;
      opacity: 0.85;
      margin-top: 2px;
    }
    .go-chat-close {
      background: none;
      border: none;
      color: white;
      font-size: 18px;
      cursor: pointer;
      opacity: 0.8;
    }
    .go-chat-close:hover { opacity: 1; }
    .go-chat-messages {
      flex: 1;
      padding: 16px;
      overflow-y: auto;
      display: flex;
      flex-direction: column;
      gap: 10px;
      background: #07090e;
    }
    .go-chat-msg {
      max-width: 80%;
      padding: 10px 14px;
      border-radius: 14px;
      font-size: 13px;
      line-height: 1.4;
      word-break: break-word;
    }
    .go-chat-msg.them {
      background: #1a2233;
      color: #e2e8f0;
      align-self: flex-start;
      border-bottom-left-radius: 4px;
    }
    .go-chat-msg.me {
      background: ${primaryColor};
      color: white;
      align-self: flex-end;
      border-bottom-right-radius: 4px;
    }
    .go-chat-time {
      font-size: 10px;
      opacity: 0.6;
      margin-top: 4px;
      display: block;
      text-align: right;
    }
    .go-chat-footer {
      padding: 12px;
      background: #0f141f;
      border-top: 1px solid rgba(255,255,255,0.08);
      display: flex;
      gap: 8px;
    }
    .go-chat-input {
      flex: 1;
      background: rgba(255,255,255,0.06);
      border: 1px solid rgba(255,255,255,0.1);
      border-radius: 10px;
      padding: 10px 14px;
      color: white;
      font-size: 13px;
      outline: none;
    }
    .go-chat-input:focus { border-color: ${primaryColor}; }
    .go-chat-send {
      background: ${primaryColor};
      border: none;
      color: white;
      border-radius: 10px;
      padding: 0 16px;
      font-weight: 600;
      cursor: pointer;
    }
    .go-chat-badge {
      font-size: 10px;
      text-align: center;
      padding: 4px;
      background: #07090e;
      color: #64748b;
      letter-spacing: 0.5px;
    }
  `;
  document.head.appendChild(style);

  // Render HTML structure
  container.innerHTML = `
    <div id="go-chat-bubble" title="Chat with us">💬</div>
    <div id="go-chat-window">
      <div class="go-chat-header">
        <div>
          <div class="go-chat-title">Live Support</div>
          <div class="go-chat-subtitle">⚡ Powered by Go Real-Time Engine</div>
        </div>
        <button class="go-chat-close" id="go-chat-close-btn">✕</button>
      </div>
      <div class="go-chat-messages" id="go-chat-msg-box">
        <div class="go-chat-msg them">
          👋 Hi there! How can we help you today?
          <span class="go-chat-time">Just now</span>
        </div>
      </div>
      <form class="go-chat-footer" id="go-chat-form">
        <input type="text" id="go-chat-text" class="go-chat-input" placeholder="Type your message..." autocomplete="off" required />
        <button type="submit" class="go-chat-send">Send</button>
      </form>
      <div class="go-chat-badge">SECURED BY GO WEBSOCKET CORE</div>
    </div>
  `;

  // UI elements
  const bubble = document.getElementById("go-chat-bubble");
  const win = document.getElementById("go-chat-window");
  const closeBtn = document.getElementById("go-chat-close-btn");
  const form = document.getElementById("go-chat-form");
  const input = document.getElementById("go-chat-text");
  const msgBox = document.getElementById("go-chat-msg-box");

  let isOpen = false;
  let socket = null;
  const visitorId = "Visitor_" + Math.floor(1000 + Math.random() * 9000);

  function toggleChat() {
    isOpen = !isOpen;
    win.style.display = isOpen ? "flex" : "none";
    bubble.innerHTML = isOpen ? "✕" : "💬";

    if (isOpen && !socket) {
      connectWebSocket();
    }
  }

  bubble.onclick = toggleChat;
  closeBtn.onclick = toggleChat;

  function connectWebSocket() {
    const wsProto = location.protocol === "https:" ? "wss:" : "ws:";
    const wsUrl = `${wsProto}//${host}/ws?username=${visitorId}&app_id=${appId}`;
    socket = new WebSocket(wsUrl);

    socket.onmessage = (event) => {
      try {
        const rawPayloads = String(event.data).split("\n").filter(p => p.trim());
        for (const raw of rawPayloads) {
          const msg = JSON.parse(raw);
          if (msg.type === "group" && msg.from_user !== visitorId) {
            appendMessage(msg.from_user + ": " + (msg.ciphertext || msg.text), "them");
          }
        }
      } catch (e) {
        console.error("SDK parse error:", e);
      }
    };
  }

  function appendMessage(text, side) {
    const div = document.createElement("div");
    div.className = `go-chat-msg ${side}`;
    const time = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    div.innerHTML = `${escapeHtml(text)}<span class="go-chat-time">${time}</span>`;
    msgBox.appendChild(div);
    msgBox.scrollTop = msgBox.scrollHeight;
  }

  form.onsubmit = (e) => {
    e.preventDefault();
    const text = input.value.trim();
    if (!text || !socket) return;

    input.value = "";
    appendMessage(text, "me");

    const payload = {
      type: "group",
      channel: "general",
      from_user: visitorId,
      ciphertext: text,
      is_encrypted: false,
      timestamp: new Date().toISOString(),
    };

    socket.send(JSON.stringify(payload));
  };

  function escapeHtml(str) {
    return str.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
})();
