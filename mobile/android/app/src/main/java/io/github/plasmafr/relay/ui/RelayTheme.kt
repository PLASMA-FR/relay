package io.github.plasmafr.relay.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp

private val Light = lightColorScheme(
    primary = Color(0xFF1D594A), onPrimary = Color.White,
    primaryContainer = Color(0xFFD5F2E5), onPrimaryContainer = Color(0xFF133D32),
    secondary = Color(0xFF566C61), secondaryContainer = Color(0xFFE6ECE6),
    background = Color(0xFFF7F8F2), onBackground = Color(0xFF18251F),
    surface = Color(0xFFF7F8F2), onSurface = Color(0xFF18251F),
    surfaceVariant = Color(0xFFE9EDE5), onSurfaceVariant = Color(0xFF536159),
    outline = Color(0xFF748177), outlineVariant = Color(0xFFD9E0D6),
)
private val Dark = darkColorScheme(
    primary = Color(0xFF9CD9BF), onPrimary = Color(0xFF063929),
    primaryContainer = Color(0xFF214C3D), onPrimaryContainer = Color(0xFFCEF2DF),
    secondary = Color(0xFFBDCFBF), secondaryContainer = Color(0xFF33443B),
    background = Color(0xFF111A16), onBackground = Color(0xFFE5ECE4),
    surface = Color(0xFF111A16), onSurface = Color(0xFFE5ECE4),
    surfaceVariant = Color(0xFF26332B), onSurfaceVariant = Color(0xFFB9C8BC),
    outline = Color(0xFF87998B), outlineVariant = Color(0xFF35443A),
)

@Composable
fun RelayTheme(darkTheme: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (darkTheme) Dark else Light,
        typography = Typography(
            headlineLarge = TextStyle(fontFamily = FontFamily.SansSerif, fontWeight = FontWeight.SemiBold, fontSize = 34.sp, lineHeight = 40.sp, letterSpacing = (-1).sp),
            headlineMedium = TextStyle(fontWeight = FontWeight.SemiBold, fontSize = 28.sp, lineHeight = 34.sp, letterSpacing = (-0.7).sp),
            titleLarge = TextStyle(fontWeight = FontWeight.SemiBold, fontSize = 21.sp, lineHeight = 28.sp),
            titleMedium = TextStyle(fontWeight = FontWeight.SemiBold, fontSize = 16.sp, lineHeight = 24.sp),
            bodyLarge = TextStyle(fontSize = 16.sp, lineHeight = 24.sp),
            bodyMedium = TextStyle(fontSize = 14.sp, lineHeight = 21.sp),
            labelLarge = TextStyle(fontWeight = FontWeight.SemiBold, fontSize = 14.sp, lineHeight = 20.sp),
        ),
        content = content,
    )
}
