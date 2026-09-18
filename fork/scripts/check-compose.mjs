let input = "";
process.stdin.setEncoding("utf8");
for await (const chunk of process.stdin) input += chunk;

const config = JSON.parse(input);
const backend = config.services?.backend;
const frontend = config.services?.frontend;
if (backend?.image !== "multica-fork-backend:immutable-test") throw new Error("backend image overlay missing");
if (frontend?.image !== "multica-fork-web:immutable-test") throw new Error("web image overlay missing");
const uploads = backend?.volumes?.find((volume) => volume.target === "/app/data/uploads");
if (!uploads || uploads.type !== "bind") throw new Error("uploads bind overlay missing");
if (backend?.environment?.MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS !== "ags") {
  throw new Error("External PR provider default missing");
}
console.log("compose_overlay=ok");
console.log(`backend_image=${backend.image}`);
console.log(`web_image=${frontend.image}`);
console.log("uploads=/app/data/uploads:bind");
