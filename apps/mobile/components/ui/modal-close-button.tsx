/**
 * Close icon (✕) rendered in the modal Stack header. Matches the iOS
 * "close-in-a-circle" pattern used by Linear / Things on mobile create
 * sheets — visually pairs with the submit button on the opposite side.
 *
 * Used by `[workspace]/_layout.tsx` for the new-issue and search modals.
 */
import { Pressable, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import { router } from "expo-router";

export function ModalCloseButton() {
  return (
    <Pressable
      onPress={() => router.back()}
      hitSlop={8}
      accessibilityLabel="Close"
      className="active:opacity-60"
    >
      <View className="size-7 items-center justify-center rounded-full bg-secondary">
        <Ionicons name="close" size={18} color="#3f3f46" />
      </View>
    </Pressable>
  );
}
