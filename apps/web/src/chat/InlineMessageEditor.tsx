import { EditorContent } from "@tiptap/react";
import { useCallback, useState } from "react";

import { MessageEditError } from "./chatApi";
import type { MentionTarget, Message } from "./chatTypes";
import ComposerToolbar from "./ComposerToolbar";
import { richTextToTiptapDoc, type CodecFormat } from "./tiptapSerializer";
import { useChatEditor } from "./useChatEditor";
import type { SendResult } from "./useMessages";

interface InlineMessageEditorProps {
  message: Message;
  mentionTarget?: MentionTarget;
  onSave: (body: string, format: CodecFormat) => Promise<Message>;
  onCancel: () => void;
  onForbidden: () => void;
}

export default function InlineMessageEditor({
  message,
  mentionTarget,
  onSave,
  onCancel,
  onForbidden,
}: InlineMessageEditorProps) {
  const format: CodecFormat = message.bodyFormat === "v3" ? "v3" : "v2";
  const [initialContent] = useState(() =>
    richTextToTiptapDoc(message.bodyText, message.bodyFormat),
  );
  const [error, setError] = useState<string | null>(null);
  const save = useCallback(
    async (body: string): Promise<SendResult> => {
      setError(null);
      try {
        await onSave(body, format);
        return { status: "sent" };
      } catch (requestError) {
        if (requestError instanceof MessageEditError) {
          if (requestError.reason === "window_expired") setError("Janela de edição expirada.");
          else if (requestError.reason === "rate_limited")
            setError("Aguarde antes de editar novamente.");
          else if (requestError.reason === "forbidden") onForbidden();
          else setError("Mensagem não encontrada.");
        } else {
          setError("Não foi possível editar a mensagem.");
        }
        throw requestError;
      }
    },
    [format, onForbidden, onSave],
  );
  const { editor, canSend, sending, handleSend } = useChatEditor({
    placeholder: "Editar mensagem",
    disabled: false,
    mentionTarget,
    bodyFormat: format,
    initialContent,
    clearOnSend: false,
    testId: `chat-edit-input-${message.id}`,
    onSend: save,
  });

  return (
    <div
      className="chat-msg-area__inline-edit"
      onKeyDownCapture={(event) => {
        if (event.key === "Escape") onCancel();
      }}
    >
      <EditorContent editor={editor} />
      <div className="chat-msg-area__inline-edit-bar">
        <ComposerToolbar editor={editor ?? null} disabled={sending} />
        <button type="button" onClick={onCancel} disabled={sending}>
          Cancelar
        </button>
        <button
          type="button"
          className="chat-msg-area__inline-save"
          onClick={() => void handleSend()}
          disabled={!canSend}
        >
          Salvar
        </button>
      </div>
      {error && <p role="alert">{error}</p>}
    </div>
  );
}
