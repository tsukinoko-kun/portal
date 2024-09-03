const chunkSize = 1024 * 1024; // 1MB chunks

/** @returns {Promise<WebSocket>} */
function getWs() {
    return new Promise((resolve, reject) => {
        const url = new URL(window.location.href);
        url.protocol = url.protocol === "http:" ? "ws:" : "wss:";
        url.pathname = "/ws";
        const ws = new WebSocket(url.href);

        ws.addEventListener("open", function () {
            console.log("WS Connected");
            resolve(ws);
        })

        ws.addEventListener("close", function () {
            console.log("WS Disconnected");
        })

        ws.addEventListener("error", function (error) {
            console.error("WS Error:", error);
            reject(error);
        })
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
        try {
            await sendFile(file);
        } catch (err) {
            console.error("Error sending file:", err);
        }
    }
    document.body.classList.remove("sending");
    fileInput.value = "";
});

/** @param {File} file */
const sendFile = (file) => new Promise(async (resolve, reject) => {
    const ws = await getWs();
    const fileReader = new FileReader();
    let offset = 0;

    ws.onmessage = function (event) {
        switch (event.data) {
            case "READY":
                readChunk(file);
                break;
            case "EOF":
                resolve();
                break;
            default:
                reject(event.data);
        }
    };

    function readChunk(file) {
        if (offset >= file.size) {
            ws.send("EOF");
            return;
        }
        const slice = file.slice(offset, offset + chunkSize);
        fileReader.readAsArrayBuffer(slice);
    }

    fileReader.onload = function (e) {
        ws.send(e.target.result);
        offset += e.target.result.byteLength;
    };

    const header = composeHeader(file);
    ws.send(header);
});

/** @param file {File} */
function composeHeader(file) {
    const header = {
        name: file.name,
        size: file.size,
    };
    return JSON.stringify(header);
}

document.body.addEventListener("dragenter", handleDragEnter);
document.body.addEventListener("dragover", handleDragOver);
document.body.addEventListener("dragleave", handleDragLeave);
document.body.addEventListener("drop", handleDrop);

function handleDragEnter() {
    document.body.classList.add("drag-over");
}

function handleDragOver() {
    document.body.classList.add("drag-over");
}

function handleDragLeave() {
    document.body.classList.remove("drag-over");
}

function handleDrop() {
    document.body.classList.remove("drag-over");
}
