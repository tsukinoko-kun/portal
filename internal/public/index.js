// ui elements
const fileIcons = document.getElementById("fileIcons");
if (!fileIcons) {
    throw new Error("fileIcons element not found");
}

/** @type {HTMLInputElement} */
const fileInput = document.getElementById("fileInput");
if (!fileInput) {
    throw new Error("fileInput element not found");
}

const bandwidthIndicator = document.getElementById("bandwidthIndicator")

// types

/**
 * @typedef {{name: string, size: number, lastModified: number, mime: string}} Header
 */

/** manages bandwidth indicator */
const bandwidthCalculator = {
    i: 0,
    measurements: new Array(16).fill(0),
    addMeasurement(time, data) {
        this.measurements[this.i] = data / time;
        this.i++;
        if (this.i >= this.measurements.length) {
            this.i = 0;
            this.measurements.sort((a, b) => b - a);
            const median = this.measurements[8];
            bandwidthIndicator.innerText = formatBytePerMillisecond(median);
        }
    }
};

/** handles errors sent from the Go server */
class ServerError extends Error {
    constructor(message) {
        super("unexpected message from server: " + message);
        this._serverMessage = message;
    }

    toString() {
        return this._serverMessage;
    }

    valueOf() {
        return this._serverMessage;
    }
}

/** implements the portal protocol */
class Transmitter {
    constructor() {
        const url = new URL(window.location.href);
        url.pathname = "/ws";
        url.hash = "";
        url.search = "";
        url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
        this.ws = new BlockingWebSocket(url);
        const expProm = explodedPromise();
        this.available = expProm.promise;
        this.ws.addEventListener("open", expProm.resolve, { once: true })
        this.sentCount = 0;
    }

    /**
     * @param busy {boolean}
     */
    setBusy(busy) {
        if (busy) {
            const av = explodedPromise();
            this.available = av.promise;
            this.setFree = av.resolve;
        } else {
            if (this.setFree) {
                this.setFree();
            } else {
                this.available = Promise.resolve();
            }
        }
    }

    async streamReaderToWs(reader, header) {
        let totalBytesSent = 0;
        let lastTime = performance.now();
        let lastData = 0;
        while (true) {
            const { value, done } = await reader.read();
            if (done) {
                break;
            }

            totalBytesSent += value.byteLength;
            this.ws.send(value);

            updateFileVis(header, totalBytesSent / header.size);

            if (this.sentCount >= 64) {
                this.sentCount = 0;
                const nowTime = performance.now();
                bandwidthCalculator.addMeasurement(nowTime - lastTime, totalBytesSent - lastData);
                lastTime = nowTime;
                lastData = totalBytesSent;

                await delay(0);
            } else {
                this.sentCount++;
            }
        }
    }

    /**
     * @param matcher {(data: any)=>boolean}
     * @returns {Promise<unknown>}
     */
    serverMessage(matcher) {
        return new Promise((resolve, reject) => {
            this.ws.addEventListener("message", (ev) => {
                if (matcher(ev.data)) {
                    resolve(ev.data);
                } else {
                    reject(new ServerError(ev.data));
                }
            }, { once: true });
        });
    }

    /**
     * @param file {File}
     * @param name {string}
     */
    async transmit(file, name = file.webkitRelativePath || file.name) {
        await this.available;
        this.setBusy(true);

        /** @type {Header} */
        const header = {
            name,
            size: file.size,
            lastModified: file.lastModified,
            mime: file.type,
        };

        console.debug("transmit", file, header);
        createFileVis(header);
        const fileStream = file.stream();
        const reader = fileStream.getReader();

        const serverReady = this.serverMessage((data) => data === "READY");
        this.ws.send(JSON.stringify(header));
        await serverReady;


        await this.streamReaderToWs(reader, header);
        console.debug("EOF", header.name)
        this.ws.send("EOF");
        removeFileVis(header);
        this.setBusy(false);
    }

    end() {
        console.debug("sending EOT");
        this.ws.send("EOT");
    }
}

class BlockingWebSocket {
    constructor(url, bufferedThreshold = 1024 * 1024 * 2) { // Default threshold: 2MiB
        this.ws = new WebSocket(url);
        this.bufferedThreshold = bufferedThreshold;
    }

    async send(data) {
        // Wait until bufferedAmount is below the threshold
        while (this.ws.bufferedAmount > this.bufferedThreshold) {
            await this.waitForBuffer();
        }

        // Now it's safe to send
        this.ws.send(data);
    }

    waitForBuffer() {
        return new Promise(resolve => {
            // Use setTimeout to poll bufferedAmount after a short delay
            const checkBuffer = () => {
                if (this.ws.bufferedAmount <= this.bufferedThreshold) {
                    resolve();
                } else {
                    // Keep waiting if bufferedAmount is still high
                    setTimeout(checkBuffer, 50);
                }
            };
            checkBuffer();
        });
    }

    addEventListener(event, listener, options) {
        this.ws.addEventListener(event, listener, options);
    }
}

// input element

fileInput.addEventListener("change", async function () {
    if (fileInput.files.length === 0) {
        return;
    }

    const t = new Transmitter();
    document.body.classList.add("sending");
    for (const file of fileInput.files) {
        await t.transmit(file);
    }
    t.end();
    document.body.classList.remove("sending");
    fileInput.value = "";
});

// drag and drop

document.body.addEventListener("dragenter", handleDragEnter, { passive: true });
document.body.addEventListener("dragover", handleDragOver);
document.body.addEventListener("dragleave", handleDragLeave, { passive: true });
document.body.addEventListener("drop", handleDrop);

function handleDragEnter() {
    document.body.classList.add("drag-over");
}

/** @param {DragEvent} ev */
function handleDragOver(ev) {
    ev.preventDefault();
    document.body.classList.add("drag-over");
}

function handleDragLeave() {
    document.body.classList.remove("drag-over");
}

/** @param {DragEvent} ev */
async function handleDrop(ev) {
    ev.stopPropagation();
    ev.preventDefault();
    document.body.classList.remove("drag-over");
    document.body.classList.add("sending");

    const items = Array.from(ev.dataTransfer.items).map((item) => ({
        item,
        entry: item.webkitGetAsEntry(),
    }));

    if (items.length === 0) {
        return;
    }

    document.body.classList.add("sending");

    const t = new Transmitter();

    for (const i of items) {
        if (i.entry) {
            if (i.entry.isFile) {
                try {
                    const file = await getFileFromEntry(i.entry);
                    await t.transmit(file);
                } catch (err) {
                    console.error("Failed to process file entry:", i.entry, err);
                }
            } else if (i.entry.isDirectory) {
                try {
                    for await (const file of readDirectoryRecursively(i.entry)) {
                        await t.transmit(file.file, file.name);
                    }
                } catch (err) {
                    console.error("Failed to process directory entry:", i.entry, err);
                }
            } else {
                console.error("Unsupported entry type:", i.entry);
            }
        } else {
            console.error("Failed to get entry from item", i);
        }
    }

    t.end();

    document.body.classList.remove("sending");
}

/** Helper function to get file from entry */
async function getFileFromEntry(entry) {
    return new Promise((resolve, reject) => {
        entry.file(resolve, reject);
    });
}

/** Recursively read a directory entry */
async function* readDirectoryRecursively(directoryEntry) {
    const reader = directoryEntry.createReader();
    const entries = await readAllEntries(reader);

    for (const entry of entries) {
        if (entry.isFile) {
            try {
                const file = await getFileFromEntry(entry);
                yield { file, name: entry.fullPath };
            } catch (err) {
                console.error("Failed to process file within directory:", entry, err);
            }
        } else if (entry.isDirectory) {
            for await (const f of readDirectoryRecursively(entry)) {
                yield f;
            }
        }
    }
}

/** Utility function to read all entries from a directory */
function readAllEntries(reader) {
    return new Promise((resolve, reject) => {
        const entries = [];

        function readEntries() {
            reader.readEntries((results) => {
                if (!results.length) {
                    resolve(entries);
                } else {
                    entries.push(...results);
                    readEntries(); // Continue reading until all entries are read
                }
            }, reject);
        }

        readEntries();
    });
}

/** @param {Header} header */
function createFileVis(header) {
    const fileEl = document.createElement("div");
    fileEl.classList.add("file");
    fileEl.id = btoa(header.name);

    const iconEl = document.createElement("img");
    iconEl.src = getIcon(header);
    fileEl.appendChild(iconEl);

    const statusEl = document.createElement("span");
    statusEl.innerText = "0 %";
    fileEl.appendChild(statusEl);

    fileIcons.appendChild(fileEl);
    return fileEl;
}

/**
 * @param header {Header}
 * @param progress {number}
 */
function updateFileVis(header, progress) {
    const opacity = clamp(0, 1 - progress, 1).toFixed(2);
    if (opacity <= 0) {
        removeFileVis(header);
        return;
    }
    let fileEl = document.getElementById(btoa(header.name));
    if (!fileEl) {
        fileEl = createFileVis(header);
    }

    const iconEl = fileEl.querySelector("img");
    iconEl.style.opacity = opacity;

    const statusEl = fileEl.querySelector("span");
    statusEl.innerText = `${Math.round(progress * 100)} %`;
}

/**
 * @param header {Header}
 */
function removeFileVis(header) {
    const fileEl = document.getElementById(btoa(header.name));
    if (fileEl) {
        fileEl.remove();
    }
}

// utils

const fontExtensions = new Set(["ttf", "otf", "woff", "woff2"]);
const codeExtensions = new Set(["go", "rs", "ts", "js", "tsx", "jsx", "astro", "json", "json5", "jsonc", "yaml", "yml", "toml", "java", "kt", "gradle", "swift", "c", "cc", "cpp", "h", "hpp", "cs", "fs", "vb", "py", "rb", "r", "pl", "php", "php5", "lua", "sh", "ps1", "editorconfig", "gitignore", "md", "tex", "bib"]);

/**
 * @param header {Header}
 * @returns {string}
 */
function getIcon(header) {
    const ext = header.name.split(".").pop()
    if (fontExtensions.has(ext)) {
        return "/file-earmark-font.svg"
    }
    if (codeExtensions.has(ext)) {
        return "/file-earmark-code.svg"
    }
    if (ext === "pdf") {
        return "/file-earmark-pdf.svg"
    }

    if (header.mime.startsWith("image")) {
        return "/file-earmark-image.svg"
    }
    if (header.mime.startsWith("audio")) {
        return "/file-earmark-music.svg"
    }
    if (header.mime.startsWith("video")) {
        return "/file-earmark-play.svg"
    }
    if (header.mime.startsWith("text")) {
        return "/file-earmark-text.svg"
    }
    if (header.mime.startsWith("application")) {
        return "/file-earmark-binary.svg"
    }

    return "/file-earmark.svg"
}

/**
 * @param min {number}
 * @param value {number}
 * @param max {number}
 * @returns {number}
 */
function clamp(min, value, max) {
    return Math.min(Math.max(value, min), max)
}

/**
 * @param timeout {number}
 * @returns {Promise<unknown>}
 */
function delay(timeout) {
    return new Promise((res) => {
        setTimeout(res, timeout);
    });
}

function explodedPromise() {
    let resolve, reject;
    const promise = new Promise((res, rej) => {
        resolve = res;
        reject = rej;
    });
    return {
        promise,
        resolve,
        reject,
    }
}

const factor = 1000 / (1024 * 1024);

function formatBytePerMillisecond(bytePerMilli) {
    return (bytePerMilli * factor).toFixed(1) + " MB/s";
}
