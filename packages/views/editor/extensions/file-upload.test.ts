import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Editor } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import { Markdown } from "@tiptap/markdown";
import type { UploadResult } from "@multica/core/hooks/use-file-upload";
import { ImageExtension } from "./index";
import { uploadAndInsertFile } from "./file-upload";

const BLOB_URL = "blob:test-image";
const FINAL_URL = "https://cdn.example.com/photo.png";

let editors: Editor[] = [];
let originalCreateObjectURL: typeof URL.createObjectURL | undefined;
let originalRevokeObjectURL: typeof URL.revokeObjectURL | undefined;

function makeEditor() {
  const element = document.createElement("div");
  document.body.appendChild(element);
  const editor = new Editor({
    element,
    extensions: [
      StarterKit,
      ImageExtension,
      Markdown.configure({ indentation: { style: "space", size: 3 } }),
    ],
  });
  editors.push(editor);
  return editor;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function makeUpload(
  overrides: Partial<UploadResult> & {
    id: string;
    link: string;
    filename: string;
  },
): UploadResult {
  return {
    workspace_id: "ws-1",
    issue_id: null,
    comment_id: null,
    chat_session_id: null,
    chat_message_id: null,
    uploader_type: "member",
    uploader_id: "user-1",
    url: overrides.link,
    download_url: overrides.link,
    content_type: "image/png",
    size_bytes: 1,
    created_at: new Date(0).toISOString(),
    ...overrides,
  };
}

beforeEach(() => {
  originalCreateObjectURL = URL.createObjectURL;
  originalRevokeObjectURL = URL.revokeObjectURL;
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: vi.fn(() => BLOB_URL),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: vi.fn(),
  });
});

afterEach(() => {
  for (const editor of editors) editor.destroy();
  editors = [];
  document.body.innerHTML = "";

  if (originalCreateObjectURL) {
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: originalCreateObjectURL,
    });
  } else {
    delete (URL as Partial<typeof URL>).createObjectURL;
  }

  if (originalRevokeObjectURL) {
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: originalRevokeObjectURL,
    });
  } else {
    delete (URL as Partial<typeof URL>).revokeObjectURL;
  }
});

describe("uploadAndInsertFile", () => {
  it("lets typing continue in the trailing paragraph after pasted image upload preview", async () => {
    const editor = makeEditor();
    const upload = deferred<UploadResult | null>();
    const handler = vi.fn(() => upload.promise);
    const file = new File(["image"], "photo.png", { type: "image/png" });

    const uploadTask = uploadAndInsertFile(editor, file, handler);

    expect(handler).toHaveBeenCalledWith(file);
    expect(editor.state.selection.$from.parent.type.name).toBe("paragraph");

    editor.commands.insertContent("after");
    expect(editor.getMarkdown().trimEnd()).toBe(
      [`![photo.png](${BLOB_URL})`, "", "after"].join("\n"),
    );

    upload.resolve(
      makeUpload({ id: "attachment-1", link: FINAL_URL, filename: "photo.png" }),
    );
    await uploadTask;

    const saved = editor.getMarkdown().trimEnd();
    expect(saved).toBe([`![photo.png](${FINAL_URL})`, "", "after"].join("\n"));

    const reparsed = makeEditor();
    reparsed.commands.setContent(saved, { contentType: "markdown" });
    expect(reparsed.getMarkdown().trimEnd()).toBe(saved);
  });
});
