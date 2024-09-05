const fileIcons = document.getElementById("file-icons");
let filesToSend = 0;

window.setInterval(() => {
    if (filesToSend === 0) {
        document.body.classList.remove("sending");
    } else {
        document.body.classList.add("sending");
    }
}, 1000)

if (!fileIcons) {
    throw new Error("file-icons not found");
}

let wsCount = 0;
const maxWsCount = 4;

/** @returns {Promise<WebSocket>} */
async function getWs() {
    if (wsCount > maxWsCount) {
        await new Promise((resolve) => {
            const interval = window.setInterval(() => {
                if (wsCount === 0) {
                    window.clearInterval(interval);
                    resolve();
                }
            }, 100);
        });
    }

    return await new Promise((resolve, reject) => {
        const url = new URL(window.location.href);
        url.protocol = url.protocol === "http:" ? "ws:" : "wss:";
        url.pathname = "/ws";
        const ws = new WebSocket(url.href);
        ws.binaryType = "arraybuffer";

        ws.addEventListener("open", function () {
            resolve(ws);
        });

        ws.addEventListener("error", function (error) {
            console.error("WS Error:", error);
            reject(error);
        });

        ws.addEventListener("close", function () {
            wsCount--;
        });
    });
}

/** @type {HTMLInputElement} */
const fileInput = document.getElementById("fileInput");
fileInput.addEventListener("change", async function () {
    if (fileInput.files.length === 0) {
        return;
    }

    document.body.classList.add("sending");
    for (const file of fileInput.files) {
        await sendFile(file);
    }
    cleanupFileVis();
    fileInput.value = "";
});

/**
 * @param {File} file
 * @param {string} overwritePath
 * @returns {Promise<void>}
 */
const sendFile = (file, overwritePath = file.webkitRelativePath ?? file.name) => new Promise(async (resolve, reject) => {
    console.debug("sendFile", {file, overwritePath});
    filesToSend++;
    const ws = await getWs();
    const compressionStream = new CompressionStream("gzip");
    const fileStream = file.stream();
    const compressedStream = fileStream.pipeThrough(compressionStream);
    const reader = compressedStream.getReader();

    ws.onmessage = function (event) {
        switch (event.data) {
            case "READY":
                resolve();
                void streamReaderToWs(file, reader, ws).finally(() => {
                    removeFileVis(file);
                    ws.send("EOF");
                });
                break;
            case "EOF":
                removeFileVis(file);
                ws.close();
                break;
            default:
                console.error("unexpected message from server:", event.data);
                reject(event.data);
                removeFileVis(file);
                break;
        }
    };

    const header = composeHeader(file, overwritePath);
    ws.send(header);
    createFileVis(file)
});

/**
 * @param file {File}
 * @param reader {ReadableStreamDefaultReader<any>}
 * @param ws {WebSocket}
 */
async function streamReaderToWs(file, reader, ws) {
    let totalBytesSent = 0;
    while (true) {
        const {value, done} = await reader.read();
        if (done) {
            break;
        }

        totalBytesSent += value.byteLength;
        updateFileVis(file, totalBytesSent / file.size);

        // Send the chunk over WebSocket
        ws.send(value);
    }
}

/**
 * @param file {File}
 * @param name {string}
 * @returns {string}
 */
function composeHeader(file, name) {
    const header = {
        name: name || file.webkitRelativePath || file.name,
        size: file.size,
        lastModified: file.lastModified,
    };
    return JSON.stringify(header);
}

document.body.addEventListener("dragenter", handleDragEnter, {passive: true});
document.body.addEventListener("dragover", handleDragOver);
document.body.addEventListener("dragleave", handleDragLeave, {passive: true});
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

    for (const i of items) {
        if (i.entry) {
            if (i.entry.isFile) {
                try {
                    const file = await getFileFromEntry(i.entry);
                    await sendFile(file);
                } catch (err) {
                    console.error("Failed to process file entry:", i.entry, err);
                }
            } else if (i.entry.isDirectory) {
                try {
                    await readDirectoryRecursively(i.entry);
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

    cleanupFileVis();
}

/** Helper function to get file from entry */
async function getFileFromEntry(entry) {
    return new Promise((resolve, reject) => {
        entry.file(resolve, reject);
    });
}

/** Recursively read a directory entry */
async function readDirectoryRecursively(directoryEntry) {
    const reader = directoryEntry.createReader();
    const entries = await readAllEntries(reader);

    for (const entry of entries) {
        if (entry.isFile) {
            try {
                const file = await getFileFromEntry(entry);
                await sendFile(file, entry.fullPath);
            } catch (err) {
                console.error("Failed to process file within directory:", entry, err);
            }
        } else if (entry.isDirectory) {
            await readDirectoryRecursively(entry);
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

/** @param {File} file */
function createFileVis(file) {
    const fileEl = document.createElement("img")
    fileEl.classList.add("file")
    fileEl.src = getIcon(file)
    fileEl.id = file.webkitRelativePath || file.name
    fileIcons.appendChild(fileEl)
    return fileEl
}

const fontExtensions = new Set(["ttf", "otf", "woff", "woff2"]);
const codeExtensions = new Set(["go", "rs", "ts", "js", "tsx", "jsx", "astro", "json", "json5", "jsonc", "yaml", "yml", "toml", "java", "kt", "gradle", "swift", "c", "cc", "cpp", "h", "hpp", "cs", "fs", "vb", "py", "rb", "r", "pl", "php", "php5", "lua", "sh", "ps1", "editorconfig", "gitignore", "md", "tex", "bib"]);

function getIcon(file) {
    const ext = file.name.split(".").pop()
    if (fontExtensions.has(ext)) {
        return "/file-earmark-font.svg"
    }
    if (codeExtensions.has(ext)) {
        return "/file-earmark-code.svg"
    }
    if (ext === "pdf") {
        return "/file-earmark-pdf.svg"
    }

    const mime = file.type
    if (mime.startsWith("image")) {
        return "/file-earmark-image.svg"
    }
    if (mime.startsWith("audio")) {
        return "/file-earmark-music.svg"
    }
    if (mime.startsWith("video")) {
        return "/file-earmark-play.svg"
    }
    if (mime.startsWith("text")) {
        return "/file-earmark-text.svg"
    }
    if (mime.startsWith("application")) {
        return "/file-earmark-binary.svg"
    }

    return "/file-earmark.svg"
}

/**
 * @param file {File}
 * @param progress {number}
 */
function updateFileVis(file, progress) {
    let fileEl = document.getElementById(file.webkitRelativePath || file.name)
    if (!fileEl) {
        fileEl = createFileVis(file)
    }
    fileEl.style.opacity = clamp(0, 1 - progress, 1).toFixed(2);
}

function removeFileVis(file) {
    const fileEl = document.getElementById(file.webkitRelativePath || file.name)
    if (fileEl) {
        filesToSend--;
        fileEl.remove()
    }
}

function cleanupFileVis() {
    const files = Array.from(document.getElementsByClassName("file"))

    for (const file of files) {
        file.remove()
    }
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
