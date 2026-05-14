import { View } from "react-native";
import { Text } from "@/components/ui/text";

/**
 * Favorites placeholder. Real implementation deferred — list of pinned
 * issues / projects / views, mirroring the web Favorites surface.
 */
export default function FavoritesPage() {
  return (
    <View className="flex-1 items-center justify-center bg-background px-6">
      <Text className="text-sm text-muted-foreground text-center">
        Favorites coming soon.
      </Text>
    </View>
  );
}
