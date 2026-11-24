"use strict";

(() => {
  const byId = (id) => document.getElementById(id);
  const resourceSegments = location.pathname.split("/").filter(Boolean);
  const pluginID = decodeURIComponent(resourceSegments[resourceSegments.length - 2] || "tool-emulation");
  const endpoint = "/v8/management/config/plugins/configs/" + encodeURIComponent(pluginID);
  const state = { key: "", config: null, rules: [], etag: "", saved: "", busy: false, controller: null, catalog: [], catalogBusy: false, catalogController: null };
  byId("endpoint").textContent = endpoint;

  function notify(message, kind = "") {
    const status = byId("status");
    status.textContent = message;
    status.className = "status" + (kind ? " " + kind : "");
  }

  function refreshControls() {
    const connected = state.config !== null;
    byId("settings").disabled = state.busy || !connected;
    byId("load-button").disabled = state.busy;
    byId("management-key").disabled = state.busy || connected;
    byId("load-button").textContent = connected ? "已连接" : "连接并加载";
    byId("load-button").hidden = connected;
    byId("disconnect-button").hidden = !connected && !state.busy;
    byId("reload-button").disabled = state.busy || !connected;
    byId("save-button").disabled = state.busy || !connected;
    byId("refresh-catalog").disabled = state.busy || !connected || state.catalogBusy;
    byId("connection-state").textContent = state.busy ? "处理中" : connected ? "已连接" : "未连接";
    byId("connection-state").className = "pill" + (connected ? " connected" : "");
  }

  function setBusy(busy) {
    state.busy = busy;
    refreshControls();
  }

  function object(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value);
  }

  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function integer(id, label, minimum, maximum) {
    const raw = byId(id).value.trim();
    const value = Number(raw);
    if (!raw || !Number.isSafeInteger(value) || (minimum !== undefined && value < minimum) || (maximum !== undefined && value > maximum)) {
      throw new Error(label + "必须是有效整数" + (minimum !== undefined ? "（" + minimum + "–" + maximum + "）" : "") + "。");
    }
    return value;
  }

  function lines(value) {
    return value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
  }

  function payload(validate = false) {
    const result = clone(state.config || {});
    result.enabled = byId("enabled").checked;
    result.priority = integer("priority", "优先级");
    result.parse_retry = integer("parse-retry", "额外解析重试次数", 0, 8);
    result.max_stream_buffer_bytes = integer("stream-buffer-bytes", "流式原始缓冲上限", 1024, 134217728);
    result.on_parse_failure = byId("parse-failure").value;
    result.prefix_cache = byId("prefix-cache").checked;
    result.cache_ttl = byId("cache-ttl").value;
    if (!["error", "parse_failed_to_content"].includes(result.on_parse_failure) || !["5m", "1h"].includes(result.cache_ttl)) {
      throw new Error("解析策略或缓存 TTL 无效，请重新选择。");
    }
    result.rules = state.rules.map((rule, index) => {
      const copy = clone(rule);
      const row = byId("rules").children[index];
      const provider = row.querySelector(".rule-provider").value.trim();
      const models = lines(row.querySelector(".rule-models").value);
      const aliases = lines(row.querySelector(".rule-aliases").value);
      if (provider) copy.provider = provider; else delete copy.provider;
      if (models.length) copy.models = models; else delete copy.models;
      if (aliases.length) copy.model_aliases = aliases; else delete copy.model_aliases;
      return copy;
    });
    const delimiter = byId("fence-token").value.trim();
    if (validate && delimiter && !/^[A-Za-z0-9_-]{4,32}$/.test(delimiter)) {
      throw new Error("协议分隔符须为 4–32 位 ASCII 字母、数字、下划线或连字符。");
    }
    if (delimiter) result["fence_" + "token"] = delimiter; else delete result["fence_" + "token"];
    const tags = object(result.tag_group) ? clone(result.tag_group) : {};
    for (const [name, id] of [["tool", "tag-tool"], ["arg", "tag-arg"], ["result", "tag-result"]]) {
      const tag = byId(id).value.trim();
      if (validate && tag && !/^[A-Za-z][A-Za-z0-9_-]{0,63}$/.test(tag)) {
        throw new Error("协议标签须以 ASCII 字母开头，最多 64 位，可包含数字、下划线和连字符。");
      }
      if (tag) tags[name] = tag; else delete tags[name];
    }
    if (validate && new Set([tags.tool || "CPA_TC", tags.arg || "CPA_TA", tags.result || "CPA_TR"]).size !== 3) {
      throw new Error("应用默认值后，工具、参数和结果标签必须互不相同。");
    }
    if (Object.keys(tags).length) result.tag_group = tags; else delete result.tag_group;
    return result;
  }

  function updatePreview() {
    if (!state.config) return;
    try {
      const text = JSON.stringify(payload(), null, 2);
      byId("config-preview").textContent = text;
      const configured = state.rules.some((rule, index) => [".rule-models", ".rule-aliases"].some((selector) => lines(byId("rules").children[index].querySelector(selector).value).length > 0));
      byId("dirty-state").textContent = !configured ? "未配置模型：不会接管请求" : text === state.saved ? "已同步宿主配置" : "有未保存的修改";
    } catch (error) {
      byId("config-preview").textContent = error.message;
      byId("dirty-state").textContent = "配置需要修正";
    }
  }

  function syncRuleValues() {
    if (state.config) state.rules = payload().rules;
  }

  function ruleInput(label, className, value, multiline, id) {
    const wrapper = document.createElement("label");
    wrapper.className = "field" + (className === "rule-provider" ? " provider-field" : "");
    wrapper.htmlFor = id;
    const title = document.createElement("span");
    title.textContent = label;
    const input = document.createElement(multiline ? "textarea" : "input");
    input.id = id;
    input.className = className;
    input.value = value;
    input.spellcheck = false;
    if (!multiline) { input.type = "text"; input.setAttribute("list", "native-providers"); }
    input.placeholder = multiline ? "每行一个匹配模式，也可用逗号分隔" : "留空由宿主正常路由";
    wrapper.append(title, input);
    return wrapper;
  }

  function renderRules() {
    const container = byId("rules");
    container.replaceChildren();
    byId("empty-rules").hidden = state.rules.length > 0;
    state.rules.forEach((rule, index) => {
      const row = document.createElement("article");
      row.className = "rule";
      const head = document.createElement("div");
      head.className = "rule-head";
      const title = document.createElement("span");
      title.className = "rule-title";
      title.textContent = "规则 " + (index + 1);
      const actions = document.createElement("div");
      actions.className = "rule-actions";
      for (const [label, offset] of [["上移", -1], ["下移", 1], ["删除", 0]]) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "button secondary" + (!offset ? " remove" : "");
        button.textContent = label;
        button.setAttribute("aria-label", label + "规则 " + (index + 1));
        button.disabled = offset < 0 && index === 0 || offset > 0 && index === state.rules.length - 1;
        button.addEventListener("click", () => {
          try {
            syncRuleValues();
            if (!offset) state.rules.splice(index, 1);
            else [state.rules[index], state.rules[index + offset]] = [state.rules[index + offset], state.rules[index]];
            renderRules();
            updatePreview();
          } catch (error) { notify(error.message, "error"); }
        });
        actions.append(button);
      }
      head.append(title, actions);
      const grid = document.createElement("div");
      grid.className = "rule-grid";
      grid.append(ruleInput("模型匹配", "rule-models", (rule.models || []).join("\n"), true, "rule-models-" + index), ruleInput("模型别名匹配", "rule-aliases", (rule.model_aliases || []).join("\n"), true, "rule-aliases-" + index));
      row.append(head, ruleInput("原生提供商键（可选）", "rule-provider", rule.provider || "", false, "rule-provider-" + index), grid);
      row.append(createModelPicker(row));
      container.append(row);
    });
  }

  function showConfiguration(config) {
    if (!object(config)) throw new Error("宿主返回的插件配置不是对象。");
    const rules = config.rules === undefined || config.rules === null ? [] : config.rules;
    if (!Array.isArray(rules) || rules.some((rule) => !object(rule) || ["models", "model_aliases"].some((key) => rule[key] !== undefined && rule[key] !== null && (!Array.isArray(rule[key]) || rule[key].some((value) => typeof value !== "string"))))) {
      throw new Error("现有规则格式不受此编辑器支持，请先通过宿主配置编辑器修正。");
    }
    state.config = clone(config);
    state.rules = clone(rules).map((rule) => {
      if (rule.models === null) delete rule.models;
      if (rule.model_aliases === null) delete rule.model_aliases;
      return rule;
    });
    byId("enabled").checked = config.enabled !== false;
    byId("priority").value = config.priority ?? 0;
    byId("parse-retry").value = config.parse_retry ?? 1;
    byId("stream-buffer-bytes").value = config.max_stream_buffer_bytes ?? 8388608;
    byId("parse-failure").value = config.on_parse_failure ?? "parse_failed_to_content";
    byId("prefix-cache").checked = config.prefix_cache !== false;
    byId("cache-ttl").value = config.cache_ttl ?? "5m";
    byId("fence-token").value = config.fence_token ?? "";
    const tags = object(config.tag_group) ? config.tag_group : {};
    byId("tag-tool").value = tags.tool ?? "";
    byId("tag-arg").value = tags.arg ?? "";
    byId("tag-result").value = tags.result ?? "";
    renderRules();
    state.saved = JSON.stringify(payload(), null, 2);
    updatePreview();
  }

  async function request(method, body, path = endpoint, key = state.key, signal = state.controller.signal) {
    const headers = { "Authorization": "Bearer " + key, "Accept": "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (method !== "GET" && state.etag) headers["If-Match"] = state.etag;
    const response = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), ["cred" + "entials"]: "omit", cache: "no-store", redirect: "error", signal });
    const text = await response.text();
    if (!response.ok) {
      if (response.status === 401 || response.status === 403) throw new Error(path === "/v1/models" ? "模型查询鉴权失败：请检查 CPA 客户端 API key。" : "管理鉴权失败：请检查管理密钥和远程管理权限。");
      if (response.status === 409 || response.status === 412) throw new Error("配置已被其他页面修改。请先重新加载，再保存本页修改。");
      if (response.status === 404 && method === "GET" && path === endpoint) throw new Error("未找到插件配置。请确认插件已安装，并已在宿主配置中启用。");
      let detail = "";
      try { const error = JSON.parse(text); detail = typeof error.message === "string" ? error.message : typeof error.error === "string" ? error.error : ""; } catch (_) {}
      throw new Error("管理接口返回 HTTP " + response.status + (detail ? "：" + detail : "。"));
    }
    if (method === "GET" && path === endpoint) state.etag = response.headers.get("ETag") || "";
    return text ? JSON.parse(text) : {};
  }

  async function load() {
    const config = await request("GET");
    showConfiguration(config);
    refreshControls();
  }

  function createModelPicker(row) {
    const picker = document.createElement("details");
    picker.className = "model-picker";
    const summary = document.createElement("summary");
    summary.textContent = "从 CPA 模型目录选择";
    const controls = document.createElement("div");
    controls.className = "picker-controls";
    const search = document.createElement("input");
    search.type = "search";
    search.className = "picker-search";
    search.placeholder = "搜索模型 ID / 提供商";
    search.setAttribute("aria-label", "搜索 CPA 模型");
    const target = document.createElement("select");
    target.className = "picker-target";
    target.setAttribute("aria-label", "选择写入字段");
    for (const [value, label] of [[".rule-models", "写入模型匹配"], [".rule-aliases", "写入别名匹配"]]) {
      const option = document.createElement("option"); option.value = value; option.textContent = label; target.append(option);
    }
    const list = document.createElement("div");
    list.className = "picker-list";
    controls.append(search, target);
    picker.append(summary, controls, list);
    search.addEventListener("input", () => renderModelPicker(row));
    target.addEventListener("change", () => renderModelPicker(row));
    row.querySelectorAll(".rule-models, .rule-aliases").forEach((input) => input.addEventListener("input", () => renderModelPicker(row)));
    picker.addEventListener("toggle", () => { if (picker.open) renderModelPicker(row); });
    return picker;
  }

  function renderModelPicker(row) {
    const list = row.querySelector(".picker-list");
    if (!list) return;
    const search = row.querySelector(".picker-search").value.toLowerCase();
    const input = row.querySelector(row.querySelector(".picker-target").value);
    const selected = new Set(lines(input.value));
    list.replaceChildren();
    const visible = state.catalog.filter((model) => (model.id + " " + model.label).toLowerCase().includes(search));
    for (const model of visible) {
      const label = document.createElement("label"); label.className = "picker-option";
      const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.checked = selected.has(model.id);
      const text = document.createElement("span");
      const id = document.createElement("strong"); id.textContent = model.id;
      const description = document.createElement("small"); description.textContent = model.label;
      text.append(id, description); label.append(checkbox, text); list.append(label);
      checkbox.addEventListener("change", () => {
        const values = new Set(lines(input.value));
        if (checkbox.checked) values.add(model.id); else values.delete(model.id);
        input.value = [...values].join("\n");
        updatePreview();
      });
    }
    if (!visible.length) { const empty = document.createElement("p"); empty.textContent = state.catalog.length ? "没有匹配的模型。" : "模型目录为空；可刷新模型，或填入客户端 API key 获取完整目录。手动填写和通配符仍可使用。"; list.append(empty); }
  }

  async function refreshCatalog() {
    if (!state.key || state.catalogBusy) return;
    const controller = new AbortController();
    state.catalogController = controller; state.catalogBusy = true;
    refreshControls();
    byId("catalog-status").textContent = "正在从 CPA 获取模型…";
    const models = new Map(); const providers = new Set(); let failed = 0;
    const add = (entries, provider = "") => {
      for (const entry of entries) {
        if (!object(entry) || typeof entry.id !== "string" || !entry.id.trim()) continue;
        const id = entry.id.trim();
        const label = [provider, entry.display_name, entry.owned_by].filter((value) => typeof value === "string" && value).join(" · ");
        const previous = models.get(id);
        models.set(id, { id, label: previous && previous.label && previous.label !== label ? previous.label + " / " + label : label || previous?.label || "CPA 已登记模型" });
      }
    };
    try {
      const directory = await request("GET", undefined, "/v8/management/credentials", state.key, controller.signal);
      if (!Array.isArray(directory.files)) throw new Error("CPA 凭据目录格式无效。");
      const files = directory.files.filter((file) => object(file) && !file.disabled && typeof file.name === "string" && file.name);
      let next = 0;
      await Promise.all(Array.from({ length: Math.min(4, files.length) }, async () => {
        while (next < files.length) {
          const file = files[next++];
          const provider = typeof file.provider === "string" ? file.provider : "";
          if (provider) providers.add(provider);
          try {
            const result = await request("GET", undefined, "/v8/management/credentials/models?name=" + encodeURIComponent(file.name), state.key, controller.signal);
            if (!Array.isArray(result.models)) throw new Error("模型目录格式无效");
            add(result.models, provider);
          } catch (error) { if (error.name === "AbortError") throw error; failed++; }
        }
      }));
      const clientAccess = byId("model-api-key").value.trim();
      if (clientAccess) {
        const result = await request("GET", undefined, "/v1/models", clientAccess, controller.signal);
        if (!Array.isArray(result.data)) throw new Error("CPA 调用模型目录格式无效。");
        add(result.data);
      }
      if (controller.signal.aborted || !state.key) return;
      state.catalog = [...models.values()].sort((a, b) => a.id.localeCompare(b.id));
      byId("native-providers").replaceChildren();
      for (const provider of [...providers].sort()) { const option = document.createElement("option"); option.value = provider; byId("native-providers").append(option); }
      for (const row of byId("rules").children) renderModelPicker(row);
      byId("catalog-status").textContent = "已获取 " + state.catalog.length + " 个模型" + (failed ? "；" + failed + " 个凭据查询失败，可重试。" : "。") + (!clientAccess ? " 内联 API key 模型可通过上方客户端 API key 补全。" : " 已包含客户端调用目录。");
    } catch (error) { if (error.name !== "AbortError") byId("catalog-status").textContent = "模型查询失败：" + error.message + " 现有规则不会被修改。"; }
    finally { if (state.catalogController === controller) { state.catalogBusy = false; refreshControls(); } }
  }

  byId("refresh-catalog").addEventListener("click", () => { void refreshCatalog(); });

  function hasChanges() {
    if (!state.config) return false;
    try { return JSON.stringify(payload(), null, 2) !== state.saved; } catch (_) { return true; }
  }

  function disconnect() {
    if (state.controller) state.controller.abort();
    if (state.catalogController) state.catalogController.abort();
    state.catalog = [];
    state.catalogBusy = false;
    byId("model-api-key").value = "";
    byId("native-providers").replaceChildren();
    byId("catalog-status").textContent = "连接后自动获取 CPA 已登记模型。";
    state.key = "";
    state.config = null;
    state.rules = [];
    state.etag = "";
    state.saved = "";
    state.busy = false;
    byId("management-key").value = "";
    byId("config-form").reset();
    byId("rules").replaceChildren();
    byId("empty-rules").hidden = false;
    byId("config-preview").textContent = "";
    byId("dirty-state").textContent = "尚未加载配置";
    refreshControls();
    notify("已断开连接并清除本页管理密钥。");
  }

  byId("connection-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (state.busy) return;
    const key = byId("management-key").value.trim();
    if (!key) { notify("请输入管理密钥。", "error"); return; }
    state.key = key;
    state.controller = new AbortController();
    setBusy(true);
    try { await load(); notify("配置已加载，可以开始编辑。", "success"); void refreshCatalog(); }
    catch (error) { if (error.name !== "AbortError") { state.key = ""; byId("management-key").value = ""; notify(error.message, "error"); } }
    finally { setBusy(false); }
  });

  byId("disconnect-button").addEventListener("click", () => {
    if (!hasChanges() || confirm("有未保存的修改，仍要断开连接吗？")) disconnect();
  });

  byId("reload-button").addEventListener("click", async () => {
    if (state.busy || hasChanges() && !confirm("重新加载会丢弃本页未保存的修改，继续吗？")) return;
    state.controller = new AbortController();
    setBusy(true);
    try { await load(); notify("已重新加载宿主配置。", "success"); }
    catch (error) { if (error.name !== "AbortError") notify(error.message, "error"); }
    finally { setBusy(false); }
  });

  byId("config-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (state.busy || !state.config) return;
    let updated;
    try { updated = payload(true); }
    catch (error) { notify(error.message, "error"); return; }
    state.controller = new AbortController();
    setBusy(true);
    try {
      await request("PUT", updated);
      await load();
      notify("配置已保存到宿主。宿主将负责配置重载；禁用插件后，其页面入口可能消失。", "success");
    } catch (error) { if (error.name !== "AbortError") notify(error.message, "error"); }
    finally { setBusy(false); }
  });

  byId("add-rule").addEventListener("click", () => {
    try { syncRuleValues(); state.rules.push({}); renderRules(); updatePreview(); }
    catch (error) { notify(error.message, "error"); }
  });
  byId("config-form").addEventListener("input", updatePreview);
  byId("config-form").addEventListener("change", updatePreview);
  window.addEventListener("beforeunload", (event) => { if (hasChanges()) { event.preventDefault(); event.returnValue = ""; } });
  window.addEventListener("pagehide", disconnect);
  refreshControls();
})();
