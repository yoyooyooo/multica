// Importing this module registers the built-in inbox item-type renderers as a
// side effect. Import it once (the inbox page does) before resolving a renderer
// via getInboxItemRenderer.
import "./issue-notification";

export {
  getInboxItemRenderer,
  hasInboxItemRenderer,
  inboxItemKind,
  registerInboxItemType,
  type InboxItemDetailProps,
  type InboxItemKind,
  type InboxItemRenderer,
  type InboxItemRowProps,
} from "./contract";
