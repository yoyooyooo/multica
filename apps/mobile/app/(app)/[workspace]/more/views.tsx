import { View } from "react-native";
import { Text } from "@/components/ui/text";

/**
 * Views placeholder. Saved-filter views surface, filled in a later phase to
 * mirror the web Views surface.
 */
export default function ViewsPage() {
  return (
    <View className="flex-1 items-center justify-center bg-background px-6">
      <Text className="text-sm text-muted-foreground text-center">
        Views coming soon.
      </Text>
    </View>
  );
}
