import DefaultTheme from 'vitepress/theme-without-fonts'

// A copy of the XCIII guide's palette and typefaces, not an import of them: the
// repositories are separate, and a guide that builds only when a neighbour is
// checked out is worse than a duplicated stylesheet. Change the tokens in both.
import './custom.css'

export default DefaultTheme
