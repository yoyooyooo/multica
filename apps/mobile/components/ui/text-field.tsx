/**
 * Single-line text input primitive. Encapsulates the four cross-platform
 * RN TextInput workarounds so screen authors don't repeat them:
 *
 *   1. `paddingVertical: 0` — sidesteps RN 0.79+ padding regression
 *      (facebook/react-native#50692, still open). The default RN TextInput
 *      adds invisible inner padding that breaks vertical centering;
 *      explicit 0 strips it.
 *   2. `includeFontPadding: false` — Android `EditText` adds top/bottom
 *      "font padding" by default for tall glyphs (Tibetan/Arabic). Latin-
 *      only UIs end up taller than expected. iOS no-op. Constant cost,
 *      future-proofs the moment we re-enable Android.
 *   3. `textAlignVertical: "center"` — Android-only signal that anchors
 *      the cursor at the vertical center of the input frame; iOS no-op.
 *   4. `multiline={false}` + `numberOfLines={1}` — locks the widget to
 *      `UITextField` / single-line `EditText` mode (different native
 *      widget from multiline; do not mix).
 *
 * Variants are minimal by design (YAGNI). Only `filled` is shipped because
 * it's the variant we actually need today (picker sheets). When `outlined`
 * or `hero` use sites land, add them then.
 *
 * Repurposed `<Input>` lives at ./input.tsx — it re-exports this with
 * the variant defaulted, kept around so any future import-by-name still
 * resolves to something reasonable.
 */
import * as React from "react";
import { TextInput, type TextInputProps } from "react-native";
import { cn } from "@/lib/utils";
import { MOBILE_PLACEHOLDER_COLOR } from "./input-tokens";

type Variant = "filled";

export interface TextFieldProps
  extends Omit<TextInputProps, "multiline" | "numberOfLines"> {
  variant?: Variant;
  className?: string;
}

export const TextField = React.forwardRef<TextInput, TextFieldProps>(
  ({ variant = "filled", className, style, ...rest }, ref) => {
    return (
      <TextInput
        ref={ref}
        multiline={false}
        numberOfLines={1}
        placeholderTextColor={MOBILE_PLACEHOLDER_COLOR}
        style={[
          {
            paddingVertical: 0,
            includeFontPadding: false,
            textAlignVertical: "center",
          },
          style,
        ]}
        className={cn(
          "text-foreground",
          variant === "filled" &&
            "bg-secondary/50 rounded-md px-3 py-2 text-sm",
          className,
        )}
        {...rest}
      />
    );
  },
);
TextField.displayName = "TextField";
